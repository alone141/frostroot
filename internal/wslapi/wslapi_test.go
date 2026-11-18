package wslapi

import (
	"errors"
	"strings"
	"testing"
)

func TestCheckHRESULT(t *testing.T) {
	for _, testCase := range []struct {
		function   string
		result     uint32
		wantErr    error // nil for a success
		wantInText string
	}{
		{functionRegister, 0, nil, ""},
		{functionRegister, 1, nil, ""}, // S_FALSE succeeds
		{functionRegister, 0x80004005, ErrTarballRefused, "WslRegisterDistribution: the tarball was refused: Windows could not unpack it (0x80004005)"},
		{functionRegister, 0x800700B7, ErrAlreadyRegistered, "already registered (0x800700B7)"},
		{functionUnregister, 0x80070005, ErrAccessDenied, "WslUnregisterDistribution: access denied (0x80070005)"},
		{functionConfigure, 0x8007019E, ErrNoWSL, "(0x8007019E)"},
		// E_FAIL from anything but a registration says nothing more.
		{functionLaunch, 0x80004005, nil, "WslLaunch failed with 0x80004005"},
		{functionLaunch, 0x8000FFFF, nil, "WslLaunch failed with 0x8000FFFF"},
	} {
		err := checkHRESULT(testCase.function, testCase.result)
		if testCase.wantInText == "" {
			if err != nil {
				t.Errorf("checkHRESULT(%s, %#x) = %v, want nil", testCase.function, testCase.result, err)
			}
			continue
		}
		var hresultErr *HRESULTError
		if !errors.As(err, &hresultErr) || hresultErr.Code != testCase.result || hresultErr.Function != testCase.function {
			t.Errorf("checkHRESULT(%s, %#x) = %#v, want an HRESULTError with the function and the code", testCase.function, testCase.result, err)
			continue
		}
		if !strings.Contains(err.Error(), testCase.wantInText) {
			t.Errorf("checkHRESULT(%s, %#x) = %q, want it to say %q", testCase.function, testCase.result, err, testCase.wantInText)
		}
		for _, sentinel := range []error{ErrTarballRefused, ErrAlreadyRegistered, ErrAccessDenied, ErrNoWSL} {
			if is := errors.Is(err, sentinel); is != errors.Is(testCase.wantErr, sentinel) {
				t.Errorf("errors.Is(checkHRESULT(%s, %#x), %v) = %v", testCase.function, testCase.result, sentinel, is)
			}
		}
	}
}
