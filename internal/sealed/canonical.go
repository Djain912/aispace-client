package sealed

import identitypkg "github.com/aispace-sh/aispace-client/internal/identity"

func canonicalRFC8785(value any) ([]byte, error) { return identitypkg.CanonicalJSON(value) }
