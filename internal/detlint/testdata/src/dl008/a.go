package dl008

import (
	"os"
	"syscall"
)

func F() {
	_ = os.Getenv("X")         // want DL008
	_, _ = os.LookupEnv("X")   // want DL008
	_ = os.Environ()           // want DL008
	_ = os.ExpandEnv("$X")     // want DL008
	_, _ = syscall.Getenv("X") // want DL008
	_ = syscall.Environ()      // want DL008
	_ = os.Getpid()
}
