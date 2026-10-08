// Package define takes two macros from #cgo lines: one from CFLAGS, read
// by the preamble and by define.c, and one from CPPFLAGS. Without them
// the code still compiles and every function returns 0.
package define

/*
#cgo CFLAGS: -DFROM_CFLAGS=7
#cgo CPPFLAGS: -DFROM_CPPFLAGS=11

#ifndef FROM_CFLAGS
#define FROM_CFLAGS 0
#endif
#ifndef FROM_CPPFLAGS
#define FROM_CPPFLAGS 0
#endif

static int preamble(void) { return FROM_CFLAGS; }
static int cppflags(void) { return FROM_CPPFLAGS; }
int cfile(void);
*/
import "C"

func Preamble() int { return int(C.preamble()) }
func CPPFlags() int { return int(C.cppflags()) }
func CFile() int    { return int(C.cfile()) }
