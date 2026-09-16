package task

import "errors"

const (
	CredentialEnvNotFoundReason  = "CREDENTIAL_ENV_NOT_FOUND"
	CredentialEnvAmbiguousReason = "CREDENTIAL_ENV_AMBIGUOUS"
)

// CredentialEnvErrorDescriptor is the transport-neutral form of a credential
// env lookup error. Transport adapters can serialize it without depending on
// concrete Go error types.
type CredentialEnvErrorDescriptor struct {
	Reason        string
	EnvName       string
	FirstProfile  string
	SecondProfile string
}

// DescribeCredentialEnvError converts a typed credential env error into its
// transport-neutral descriptor.
func DescribeCredentialEnvError(err error) (CredentialEnvErrorDescriptor, bool) {
	var notFound *CredentialEnvNotFoundError
	if errors.As(err, &notFound) {
		return CredentialEnvErrorDescriptor{
			Reason:  CredentialEnvNotFoundReason,
			EnvName: notFound.EnvName,
		}, true
	}
	var ambiguous *CredentialEnvAmbiguousError
	if errors.As(err, &ambiguous) {
		return CredentialEnvErrorDescriptor{
			Reason:        CredentialEnvAmbiguousReason,
			EnvName:       ambiguous.EnvName,
			FirstProfile:  ambiguous.FirstProfile,
			SecondProfile: ambiguous.SecondProfile,
		}, true
	}
	return CredentialEnvErrorDescriptor{}, false
}

// RestoreCredentialEnvError reconstructs a typed credential env error from a
// transport-neutral descriptor.
func RestoreCredentialEnvError(descriptor CredentialEnvErrorDescriptor) (error, bool) {
	switch descriptor.Reason {
	case CredentialEnvNotFoundReason:
		if descriptor.EnvName == "" {
			return nil, false
		}
		return &CredentialEnvNotFoundError{EnvName: descriptor.EnvName}, true
	case CredentialEnvAmbiguousReason:
		if descriptor.EnvName == "" || descriptor.FirstProfile == "" || descriptor.SecondProfile == "" {
			return nil, false
		}
		return &CredentialEnvAmbiguousError{
			EnvName:       descriptor.EnvName,
			FirstProfile:  descriptor.FirstProfile,
			SecondProfile: descriptor.SecondProfile,
		}, true
	default:
		return nil, false
	}
}
