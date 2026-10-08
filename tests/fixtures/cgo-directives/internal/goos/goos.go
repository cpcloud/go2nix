// Package goos has #cgo lines with GOOS conditions: exactly one of the
// CFLAGS lines applies to a build, and the LDFLAGS line, which names a
// library only Windows has, applies to none of the supported ones.
package goos

/*
#cgo linux CFLAGS: -DGOOS_LINUX
#cgo darwin CFLAGS: -DGOOS_DARWIN
#cgo !linux,!darwin CFLAGS: -DGOOS_OTHER
#cgo windows LDFLAGS: -lws2_32

static int count(void) {
	int n = 0;
#ifdef GOOS_LINUX
	n++;
#endif
#ifdef GOOS_DARWIN
	n++;
#endif
#ifdef GOOS_OTHER
	n++;
#endif
	return n;
}

static const char *name(void) {
#if defined(GOOS_LINUX)
	return "linux";
#elif defined(GOOS_DARWIN)
	return "darwin";
#elif defined(GOOS_OTHER)
	return "other";
#else
	return "none";
#endif
}
*/
import "C"

// Name is the GOOS the #cgo conditions selected, "none" when no line
// applied and "several" when more than one did.
func Name() string {
	if C.count() > 1 {
		return "several"
	}
	return C.GoString(C.name())
}
