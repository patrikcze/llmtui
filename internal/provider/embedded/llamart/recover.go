package llamart

import (
	"errors"
	"fmt"
)

// recoverError turns recoverable panics at the FFI boundary into ordinary Go
// errors. Native faults such as SIGSEGV are not recoverable and remain outside
// this guarantee.
func recoverError(operation string, target *error) {
	if recovered := recover(); recovered != nil {
		panicErr := fmt.Errorf("%s: recovered panic: %v", operation, recovered)
		if *target != nil {
			*target = errors.Join(*target, panicErr)
			return
		}
		*target = panicErr
	}
}
