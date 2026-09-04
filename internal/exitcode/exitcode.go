package exitcode

const (
	OK            = 0
	Usage         = 64
	InvalidSpec   = 65
	NoSuchFile    = 66
	Unreachable   = 69
	InternalError = 70
	NotAdmitted   = 75
	Unauthorized  = 77
	Misconfigured = 78
	Timeout       = 124
	Cancelled     = 125
	CouldNotStart = 126
	MissingDep    = 127
)

const (
	reservedLow  = 64
	reservedHigh = 127
)

func FromTask(code int) int {
	if code >= reservedLow && code <= reservedHigh {
		return 1
	}
	return code
}
