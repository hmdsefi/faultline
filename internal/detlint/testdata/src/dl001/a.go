package dl001

import "time"

func F() {
	_ = time.Now()  // want DL001
	f := time.Since // want DL001
	_ = f
	time.Sleep(1)                    // want DL001
	<-time.After(1)                  // want DL001
	_ = time.Tick(1)                 // want DL001
	_ = time.Until(time.Time{})      // want DL001
	_ = time.AfterFunc(1, func() {}) // want DL001
	_ = time.NewTimer(1)             // want DL001
	_ = time.NewTicker(1)            // want DL001
	_ = time.Duration(5).String()
}
