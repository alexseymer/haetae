package operator

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
)

// CallToolForTest runs one tool in a fresh conversation of the org and returns
// the JSON handed to the model and whether it succeeded.
func CallToolForTest(s *Service, org uuid.UUID, name, args string) (string, bool) {
	return s.callTool(context.Background(), &conv{id: uuid.New(), org: org}, name, json.RawMessage(args))
}
