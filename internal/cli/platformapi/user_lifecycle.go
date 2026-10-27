package platformapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"mcp-runtime/pkg/internalapi"
)

func (c *PlatformClient) DeactivateUser(ctx context.Context, userID string) (internalapi.UserDeactivationResult, error) {
	var out internalapi.UserDeactivationResult
	resp, err := c.do(ctx, http.MethodPost, "/users/"+url.PathEscape(strings.TrimSpace(userID))+"/deactivate", "", nil)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	body, err := readBody(resp.Body)
	if err != nil {
		return out, err
	}
	if resp.StatusCode != http.StatusOK {
		return out, httpAPIError(resp.StatusCode, body)
	}
	err = json.Unmarshal(body, &out)
	return out, err
}
