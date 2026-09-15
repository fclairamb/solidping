package checkssh_test

import "errors"

// errAuthRefused is what the fake server answers every password attempt with;
// the checker's probe user never has valid credentials.
var errAuthRefused = errors.New("authentication refused")
