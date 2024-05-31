package util

import (
	libconstants "github.com/filswan/go-swan-lib/constants"
)

type BasicResponse struct {
	Status  string      `json:"status"`
	Code    int         `json:"code"`
	Data    interface{} `json:"data,omitempty"`
	Message string      `json:"message,omitempty"`
}

func CreateSuccessResponse(_data interface{}) BasicResponse {
	return BasicResponse{
		Status: libconstants.SWAN_API_STATUS_SUCCESS,
		Data:   _data,
		Code:   SuccessCode,
	}
}

func CreateErrorResponse(code int, errMsg ...string) BasicResponse {
	var msg string
	if len(errMsg) == 0 {
		msg = codeMsg[code]
	} else {
		msg = errMsg[0]
	}
	return BasicResponse{
		Status:  libconstants.SWAN_API_STATUS_FAIL,
		Code:    code,
		Message: msg,
	}
}

const (
	SuccessCode = 200
	ServerError = 500

	JsonError                  = 4000
	BadParamError              = 4001
	SignatureError             = 4002
	SpaceParseResourceUriError = 4003
	CheckResourcesError        = 4004
	SpaceCheckWhiteListError   = 4005
	NoAvailableResourcesError  = 4006
	FoundJobEntityError        = 4007
	SaveJobEntityError         = 4008
	FoundWhiteListError        = 4009

	ProofParamError   = 7001
	ProofReadLogError = 7002
	ProofError        = 7003

	UbiTaskParamError    = 8001
	UbiTaskContractError = 8002
	FoundTaskEntityError = 8003
	SaveTaskEntityError  = 8004
	SubmitProofError     = 8005
)

var codeMsg = map[int]string{
	ServerError:                "Service failed",
	JsonError:                  "An error occurred while converting to json",
	BadParamError:              "The request parameter is not valid",
	SignatureError:             "Verify signature failed",
	SpaceParseResourceUriError: "An error occurred while parsing sourceUri",
	CheckResourcesError:        "An error occurred while check resources available",
	SpaceCheckWhiteListError:   "This cp does not accept tasks from wallet addresses outside the whitelist",
	NoAvailableResourcesError:  "No resources available",
	FoundJobEntityError:        "An error occurred while get job info",
	SaveJobEntityError:         "An error occurred while save job info",
	FoundWhiteListError:        "An error occurred while get whitelist",

	ProofReadLogError: "An error occurred while read the log of proof",
	ProofError:        "An error occurred while executing the calculation task",

	UbiTaskContractError: "Not found this task contract on the chain",
	FoundTaskEntityError: "An error occurred while get task info",
	SaveTaskEntityError:  "An error occurred while save task info",
	SubmitProofError:     "An error occurred while submit proof",
}
