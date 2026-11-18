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

// jOptionalAssert returns the prelude that asserts the optional interface of
// mi on recv and answers "method not found" when the implementation lacks it,
// and the receiver to call the method on. A method of the Agent or Client
// interface needs no assertion.
func jOptionalAssert(recv string, mi *ir.MethodInfo) ([]Code, string) {
	if mi.Optional == "" {
		return nil, recv
	}
	return []Code{
		List(Id("impl"), Id("ok")).Op(":=").Id(recv).Assert(Id(mi.Optional)),
		If(Op("!").Id("ok")).Block(Return(Nil(), Id("NewMethodNotFound").Call(Id("method")))),
	}, "impl"
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
