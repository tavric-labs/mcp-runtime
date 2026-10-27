package platformapi

import (
	"context"
	"fmt"
	"mcp-runtime/pkg/apiwire"
	"net/http"
)

func (c *Client) OverviewCounts(ctx context.Context) (apiwire.OverviewCounts, error) {
	var out apiwire.OverviewCounts
	status, err := c.authorizedJSON(ctx, http.MethodGet, "/internal/operations/counts", nil, &out)
	if err != nil {
		return out, err
	}
	if status != http.StatusOK {
		return out, fmt.Errorf("overview counts: status %d", status)
	}
	return out, nil
}
