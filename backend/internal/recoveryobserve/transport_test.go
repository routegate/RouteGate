package recoveryobserve

import (
	"os"
	"strings"
	"testing"
)

// The wire adapter is intentionally test-only until a future independent
// review adds a cryptographically trustworthy source of evidence and a
// safe delivery protocol. Adding a production route requires explicitly
// replacing this negative guard in a dedicated security review.
func TestRecoveryObservationTransportNotRegisteredInProductionRouters(t *testing.T) {
	for _,path:=range []string{"../http/router.go","../http/delivery_routes.go"}{
		raw,err:=os.ReadFile(path)
		if err!=nil{t.Fatal(err)}
		for _,disallowed:=range []string{
			"/api/v1/agent/recovery-observations/",
			"NewObservationTransport(",
		}{
			if strings.Contains(string(raw),disallowed){
				t.Fatalf("dormant recovery transport was accidentally exposed in %s: %q",path,disallowed)
			}
		}
	}
}
