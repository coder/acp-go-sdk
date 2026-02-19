package emit

import (
	"github.com/coder/acp-go-sdk/cmd/generate/internal/ir"
)

// invInvalid: return invalid params with compact json-like message
func jInvInvalid() Code {
	return Return(Nil(), Id("NewInvalidParams").Call(Map(String()).Any().Values(Dict{Lit("error"): Id("err").Dot("Error").Call()})))
}

// retToReqErr: wrap error to JSON-RPC request error
func jRetToReqErr() Code { return Return(Nil(), Id("toReqErr").Call(Id("err"))) }

// jUnmarshalValidate emits var p T; json.Unmarshal; p.Validate
func jUnmarshalValidate(typeName string) []Code {
	return []Code{
		Var().Id("p").Id(typeName),
		If(List(Id("err")).Op(":=").Qual("encoding/json", "Unmarshal").Call(Id("params"), Op("&").Id("p")), Id("err").Op("!=").Nil()).
			Block(jInvInvalid()),
		If(List(Id("err")).Op(":=").Id("p").Dot("Validate").Call(), Id("err").Op("!=").Nil()).
			Block(jInvInvalid()),
	}
}

// jAgentAssert returns prelude for interface assertions and the receiver name.
func jAgentAssert(binding ir.MethodBinding, methodName, paramType, respType string, hasResponse bool) ([]Code, string) {
	switch binding {
	case ir.BindAgentLoader:
		return []Code{
			List(Id("loader"), Id("ok")).Op(":=").Id("a").Dot("agent").Assert(Id("AgentLoader")),
			If(Op("!").Id("ok")).Block(Return(Nil(), Id("NewMethodNotFound").Call(Id("method")))),
		}, "loader"
	case ir.BindAgentExperimental:
		return jSingleMethodAssert(Id("a").Dot("agent"), "exp", methodName, paramType, respType, hasResponse)
	default:
		return nil, "a.agent"
	}
}

// jClientAssert returns prelude for interface assertions and the receiver name.
func jClientAssert(binding ir.MethodBinding, methodName, paramType, respType string, hasResponse bool) ([]Code, string) {
	switch binding {
	case ir.BindClientExperimental:
		return jSingleMethodAssert(Id("c").Dot("client"), "exp", methodName, paramType, respType, hasResponse)
	case ir.BindClientTerminal:
		return []Code{
			List(Id("t"), Id("ok")).Op(":=").Id("c").Dot("client").Assert(Id("ClientTerminal")),
			If(Op("!").Id("ok")).Block(Return(Nil(), Id("NewMethodNotFound").Call(Id("method")))),
		}, "t"
	default:
		return nil, "c.client"
	}
}

func jSingleMethodAssert(receiver Code, assertedName, methodName, paramType, respType string, hasResponse bool) ([]Code, string) {
	ifaceType := InterfaceFunc(func(g *Group) {
		method := g.Id(methodName).Params(Qual("context", "Context"), Id(paramType))
		if hasResponse {
			method.Params(Id(respType), Error())
		} else {
			method.Error()
		}
	})
	return []Code{
		List(Id(assertedName), Id("ok")).Op(":=").Add(receiver).Assert(ifaceType),
		If(Op("!").Id("ok")).Block(Return(Nil(), Id("NewMethodNotFound").Call(Id("method")))),
	}, assertedName
}

// Request call emitters for handlers
func jCallRequestNoResp(recv, methodName string) []Code {
	return []Code{
		If(List(Id("err")).Op(":=").Id(recv).Dot(methodName).Call(Id("ctx"), Id("p")), Id("err").Op("!=").Nil()).Block(jRetToReqErr()),
		Return(Nil(), Nil()),
	}
}

func jCallRequestWithResp(recv, methodName string) []Code {
	return []Code{
		List(Id("resp"), Id("err")).Op(":=").Id(recv).Dot(methodName).Call(Id("ctx"), Id("p")),
		If(Id("err").Op("!=").Nil()).Block(jRetToReqErr()),
		Return(Id("resp"), Nil()),
	}
}

func jCallNotification(recv, methodName string) []Code {
	return []Code{
		If(List(Id("err")).Op(":=").Id(recv).Dot(methodName).Call(Id("ctx"), Id("p")), Id("err").Op("!=").Nil()).Block(jRetToReqErr()),
		Return(Nil(), Nil()),
	}
}
