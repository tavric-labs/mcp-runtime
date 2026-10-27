package platformapi

import (
	"context"
	"fmt"
	"mcp-runtime/pkg/apikit"
	"net/http"
)

func (c *Client) OverviewCounts(ctx context.Context) (apikit.OverviewCounts, error) {
	var out apikit.OverviewCounts
	status, err := c.authorizedJSON(ctx, http.MethodGet, "/internal/operations/counts", nil, &out)
	if err != nil {
		return out, err
	}
	if status != http.StatusOK {
		return out, fmt.Errorf("overview counts: status %d", status)
	}
	return out, nil
}
