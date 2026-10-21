#include <bar.h>
#include "shim.h"

#ifndef FROM_CXXFLAGS
#define FROM_CXXFLAGS 0
#endif

int shim_header(void) { return BAR_HEADER; }
int shim_from_pc(void) { return BAR_FROM_PC; }
int shim_cxxflags(void) { return FROM_CXXFLAGS; }
