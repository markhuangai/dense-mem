package serverapp

import (
	"github.com/markhuangai/dense-mem/internal/repository"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
)

func rememberTerminalErrorInput(code rememberapp.TerminalErrorCode) repository.RememberTerminalErrorInput {
	status := rememberapp.TerminalStatusError(code)
	return repository.RememberTerminalErrorInput{
		Code: status.Code, Message: status.Message, Retryable: status.Retryable,
		NextAction: status.NextAction, Remediation: status.Remediation,
	}
}
