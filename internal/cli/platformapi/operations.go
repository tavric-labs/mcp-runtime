package platformapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"mcp-runtime/pkg/apikit"
)

type MutationOptions struct {
	NoWait         bool
	IdempotencyKey string
}

type TeamAcceptance struct {
	Team      Team             `json:"team"`
	Operation apikit.Operation `json:"operation"`
}

func (c *PlatformClient) AcceptTeamCreate(ctx context.Context, slug, name string, options MutationOptions) (TeamAcceptance, error) {
	payload, err := json.Marshal(map[string]string{"slug": strings.TrimSpace(slug), "name": strings.TrimSpace(name)})
	if err != nil {
		return TeamAcceptance{}, err
	}
	key := options.IdempotencyKey
	if key == "" {
		key = uuid.NewString()
	}
	resp, err := c.doWithHeaders(ctx, http.MethodPost, "/teams", "", bytes.NewReader(payload), http.Header{"Idempotency-Key": {key}})
	if err != nil {
		return TeamAcceptance{}, fmt.Errorf("team acceptance failed (retry with --idempotency-key %s): %w", key, err)
	}
	defer resp.Body.Close()
	body, err := readBody(resp.Body)
	if err != nil {
		return TeamAcceptance{}, fmt.Errorf("team acceptance response lost (retry with --idempotency-key %s): %w", key, err)
	}
	if resp.StatusCode != http.StatusAccepted {
		return TeamAcceptance{}, httpAPIError(resp.StatusCode, body)
	}
	var accepted TeamAcceptance
	if err := json.Unmarshal(body, &accepted); err != nil {
		return accepted, fmt.Errorf("decode team acceptance (retry with --idempotency-key %s): %w", key, err)
	}
	if accepted.Operation.ID == "" || accepted.Operation.DesiredRevisionID == "" || accepted.Team.ID == "" || accepted.Operation.ResourceID != accepted.Team.ID {
		return accepted, fmt.Errorf("invalid team acceptance (retry with --idempotency-key %s)", key)
	}
	if options.NoWait {
		return accepted, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	operation, err := c.WaitOperation(ctx, accepted.Operation.ID)
	if operation.ID != "" {
		accepted.Operation = operation
	}
	if err != nil {
		return accepted, err
	}
	if err := c.waitTeamReady(ctx, accepted); err != nil {
		return accepted, fmt.Errorf("operation %s succeeded; namespace readiness wait failed: %w", operation.ID, err)
	}
	return accepted, nil
}

// GetOperation reads the server's actor-scoped operation representation.
func (c *PlatformClient) GetOperation(ctx context.Context, id string) (apikit.Operation, error) {
	resp, err := c.do(ctx, http.MethodGet, "/operations/"+url.PathEscape(id), "", nil)
	if err != nil {
		return apikit.Operation{}, err
	}
	defer resp.Body.Close()
	body, err := readBody(resp.Body)
	if err != nil {
		return apikit.Operation{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return apikit.Operation{}, httpAPIError(resp.StatusCode, body)
	}
	var result struct {
		Operation apikit.Operation `json:"operation"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return apikit.Operation{}, err
	}
	if result.Operation.ID != id {
		return apikit.Operation{}, fmt.Errorf("operation response identity mismatch")
	}
	return result.Operation, nil
}

// WaitOperation waits only for bounded execution. Resource health is observed
// independently and is checked by the resource-specific default CLI wait.
func (c *PlatformClient) WaitOperation(ctx context.Context, id string) (apikit.Operation, error) {
	for {
		op, err := c.GetOperation(ctx, id)
		if err != nil {
			return op, fmt.Errorf("read accepted operation %s: %w", id, err)
		}
		switch op.State {
		case apikit.OperationSucceeded:
			return op, nil
		case apikit.OperationFailed, apikit.OperationSuperseded:
			return op, fmt.Errorf("accepted operation %s is %s (%s)", id, op.State, op.ErrorClass)
		case apikit.OperationPending, apikit.OperationRunning:
		default:
			return op, fmt.Errorf("accepted operation %s returned unknown state %q", id, op.State)
		}
		if err := waitPoll(ctx); err != nil {
			return op, fmt.Errorf("operation %s remains accepted: %w", id, err)
		}
	}
}

func (c *PlatformClient) waitTeamReady(ctx context.Context, accepted TeamAcceptance) error {
	for {
		resp, err := c.do(ctx, http.MethodGet, "/teams/"+url.PathEscape(accepted.Team.Slug), "", nil)
		if err != nil {
			return err
		}
		body, readErr := readBody(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			return readErr
		}
		if resp.StatusCode != http.StatusOK {
			return httpAPIError(resp.StatusCode, body)
		}
		var current struct {
			Team   Team                   `json:"team"`
			Status *apikit.ResourceStatus `json:"status"`
		}
		if err := json.Unmarshal(body, &current); err != nil {
			return err
		}
		if current.Team.ID != accepted.Team.ID {
			return fmt.Errorf("team was replaced after acceptance")
		}
		if current.Status != nil {
			status := current.Status
			if status.DesiredRevisionID != accepted.Operation.DesiredRevisionID {
				return fmt.Errorf("desired revision changed after acceptance")
			}
			if status.Health == apikit.TeamIntentHealthReady && status.AppliedRevisionID == status.DesiredRevisionID && status.ObservedRevisionID == status.DesiredRevisionID {
				return nil
			}
		}
		if err := waitPoll(ctx); err != nil {
			return err
		}
	}
}

func waitPoll(ctx context.Context) error {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
