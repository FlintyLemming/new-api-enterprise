package controller

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The Langfuse lifecycle in controller.Relay is an ordering contract, not a
// value contract: design §5.3 fixes where each hook runs relative to billing
// settlement, the client error response and the panic that Gin's Recovery must
// still see. Driving the whole relay would need a database, a channel and an
// upstream server without observing that order any more directly, so these
// tests assert the call responsibilities on the source itself.
func relayFunction(t *testing.T) *ast.FuncDecl {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), "relay.go", nil, 0)
	require.NoError(t, err)

	for _, decl := range parsed.Decls {
		if function, ok := decl.(*ast.FuncDecl); ok && function.Name.Name == "Relay" {
			return function
		}
	}
	require.FailNow(t, "controller.Relay not found")
	return nil
}

// selectorPath renders a call target such as relayInfo.Billing.Refund back into
// its dotted source form.
func selectorPath(expr ast.Expr) string {
	switch typed := expr.(type) {
	case *ast.Ident:
		return typed.Name
	case *ast.SelectorExpr:
		if prefix := selectorPath(typed.X); prefix != "" {
			return prefix + "." + typed.Sel.Name
		}
	}
	return ""
}

// callPositions returns the source positions of every call to the dotted target
// inside the node, in source order.
func callPositions(node ast.Node, target string) []token.Pos {
	var positions []token.Pos
	ast.Inspect(node, func(current ast.Node) bool {
		if call, ok := current.(*ast.CallExpr); ok && selectorPath(call.Fun) == target {
			positions = append(positions, call.Pos())
		}
		return true
	})
	return positions
}

func singleCall(t *testing.T, node ast.Node, target string) token.Pos {
	t.Helper()
	positions := callPositions(node, target)
	require.Len(t, positions, 1, "%s must be called exactly once", target)
	return positions[0]
}

// deferredCalling returns the top-level defer of Relay whose closure calls the
// dotted target.
func deferredCalling(t *testing.T, relay *ast.FuncDecl, target string) *ast.DeferStmt {
	t.Helper()
	for _, statement := range relay.Body.List {
		deferred, ok := statement.(*ast.DeferStmt)
		if !ok {
			continue
		}
		literal, ok := deferred.Call.Fun.(*ast.FuncLit)
		if !ok {
			continue
		}
		if len(callPositions(literal, target)) == 1 {
			return deferred
		}
	}
	require.FailNow(t, "no deferred closure calls "+target)
	return nil
}

// finalizer returns the deferred closure that owns the end of the request.
func finalizer(t *testing.T, relay *ast.FuncDecl) *ast.FuncLit {
	t.Helper()
	return deferredCalling(t, relay, "langfuse.Finish").Call.Fun.(*ast.FuncLit)
}

func TestRelayBeginsTracingRightAfterRelayInfo(t *testing.T) {
	relay := relayFunction(t)

	genRelayInfo := singleCall(t, relay, "relaycommon.GenRelayInfo")
	begin := singleCall(t, relay, "langfuse.Begin")
	assert.Greater(t, begin, genRelayInfo, "Begin needs the RelayInfo it reads")
	// A request rejected while billing is reserved is still traced.
	assert.Less(t, begin, singleCall(t, relay, "relay.PrepareRequestBilling"))

	// Everything the retry loop does must already be inside the trace.
	for _, statement := range relay.Body.List {
		if loop, ok := statement.(*ast.RangeStmt); ok {
			assert.Greater(t, loop.Pos(), begin)
		}
		if loop, ok := statement.(*ast.ForStmt); ok {
			assert.Greater(t, loop.Pos(), begin)
		}
	}
}

func TestRelayClosesEachAttemptInsideTheRetryLoop(t *testing.T) {
	relay := relayFunction(t)
	endAttempt := singleCall(t, relay, "langfuse.EndAttempt")

	var loop *ast.ForStmt
	for _, statement := range relay.Body.List {
		if candidate, ok := statement.(*ast.ForStmt); ok {
			loop = candidate
		}
	}
	require.NotNil(t, loop, "the retry loop is gone")
	assert.Greater(t, endAttempt, loop.Pos(), "EndAttempt belongs inside the retry loop")
	assert.Less(t, endAttempt, loop.End())

	// It must run after the handler dispatch and before the loop decides to
	// return, record a channel error or start the next round.
	// The first switch in the loop body is the handler dispatch.
	var dispatch token.Pos
	ast.Inspect(loop, func(node ast.Node) bool {
		if statement, ok := node.(*ast.SwitchStmt); ok && dispatch == token.NoPos {
			dispatch = statement.End()
		}
		return true
	})
	require.NotEqual(t, token.NoPos, dispatch, "the handler dispatch switch is gone")
	assert.Greater(t, endAttempt, dispatch, "the attempt is closed after the handler returned")

	var successReturn token.Pos
	ast.Inspect(loop, func(node ast.Node) bool {
		if statement, ok := node.(*ast.IfStmt); ok && successReturn == token.NoPos {
			if binary, ok := statement.Cond.(*ast.BinaryExpr); ok {
				if ident, ok := binary.X.(*ast.Ident); ok && ident.Name == "newAPIError" {
					successReturn = statement.Pos()
				}
			}
		}
		return true
	})
	require.NotEqual(t, token.NoPos, successReturn)
	assert.Less(t, endAttempt, successReturn, "a successful attempt is closed before the loop returns")
}

func TestRelayFinalizerOrdersBillingResponseAndTelemetry(t *testing.T) {
	relay := relayFunction(t)
	closure := finalizer(t, relay)

	setMessage := singleCall(t, closure, "newAPIError.SetMessage")
	finish := singleCall(t, closure, "langfuse.Finish")
	assert.Less(t, setMessage, finish,
		"the root output must be the normalized error the client actually received")

	// Defers run in reverse order: the refund defer is registered after the
	// finalizer, so the refund and violation fee settle before the response.
	singleCall(t, relay, "relay.RefundFailedRequestBilling")
	refund := deferredCalling(t, relay, "relay.RefundFailedRequestBilling")
	assert.Greater(t, refund.Pos(), deferredCalling(t, relay, "langfuse.Finish").Pos())
}

func TestRelayFinalizerRePanicsAfterClosingTelemetry(t *testing.T) {
	relay := relayFunction(t)
	closure := finalizer(t, relay)

	var recoverPos, markPanic, finish, rePanic token.Pos
	ast.Inspect(closure, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if ident, ok := call.Fun.(*ast.Ident); ok {
			switch ident.Name {
			case "recover":
				recoverPos = call.Pos()
			case "panic":
				rePanic = call.Pos()
			}
		}
		return true
	})
	markPanic = singleCall(t, closure, "langfuse.MarkLifecyclePanic")
	finish = singleCall(t, closure, "langfuse.Finish")

	require.NotEqual(t, token.NoPos, recoverPos, "the finalizer must detect a business panic")
	require.NotEqual(t, token.NoPos, rePanic, "the business panic must be handed to Gin's Recovery")
	assert.Less(t, recoverPos, markPanic)
	assert.Less(t, markPanic, finish, "the interrupted attempt is closed before the root freezes")
	assert.Less(t, finish, rePanic, "telemetry is closed before the panic resumes unwinding")
}

func TestRelayRefundsOnlyAfterBillingWasReserved(t *testing.T) {
	relay := relayFunction(t)

	prepare := singleCall(t, relay, "relay.PrepareRequestBilling")
	refund := deferredCalling(t, relay, "relay.RefundFailedRequestBilling")
	assert.Greater(t, refund.Pos(), prepare,
		"a request rejected before its reservation has nothing to refund")

	for _, statement := range relay.Body.List {
		if loop, ok := statement.(*ast.ForStmt); ok {
			assert.Less(t, refund.Pos(), loop.Pos(), "every attempt runs under the refund defer")
		}
	}
}
