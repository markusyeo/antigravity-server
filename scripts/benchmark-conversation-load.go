// Run with: go run ./scripts/benchmark-conversation-load.go
// Opens read-only subscriptions to the longest local conversation and checks scrollback.
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/AFSlayer/antigravity-server/internal/lsproc"
)

const service = "/exa.language_server_pb.LanguageServerService/"

type page struct {
	Indices     []int `json:"indices"`
	TotalLength int   `json:"totalLength"`
	PageBounds  struct {
		StartIndex        int `json:"startIndex"`
		EndIndexExclusive int `json:"endIndexExclusive"`
	} `json:"pageBounds"`
}

type frame struct {
	Update struct {
		MainTrajectoryUpdate struct {
			StepsUpdate *page `json:"stepsUpdate"`
		} `json:"mainTrajectoryUpdate"`
	} `json:"update"`
	Error json.RawMessage `json:"error"`
}

func readFrame(body io.Reader) (frame, int, error) {
	var header [5]byte
	if _, err := io.ReadFull(body, header[:]); err != nil {
		return frame{}, 0, err
	}
	size := int(binary.BigEndian.Uint32(header[1:]))
	if header[0] != 0 || size > 64<<20 {
		return frame{}, 0, fmt.Errorf("unexpected frame flags=%d size=%d", header[0], size)
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(body, data); err != nil {
		return frame{}, 0, err
	}
	var result frame
	err := json.Unmarshal(data, &result)
	return result, size, err
}

func run() error {
	instance, err := lsproc.Find()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	data, err := instance.Call(ctx, "GetAllCascadeTrajectories", nil)
	if err != nil {
		return err
	}
	var summaries struct {
		Trajectories map[string]struct {
			StepCount int `json:"stepCount"`
		} `json:"trajectorySummaries"`
	}
	if err := json.Unmarshal(data, &summaries); err != nil {
		return err
	}
	var conversation string
	var longest int
	for id, summary := range summaries.Trajectories {
		if summary.StepCount > longest {
			conversation, longest = id, summary.StepCount
		}
	}
	if longest <= 50 {
		return fmt.Errorf("need a local conversation with more than 50 steps")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	fmt.Printf("Longest conversation: %d steps\n", longest)
	for _, count := range []int{50, 15} {
		if err := measure(ctx, client, instance, conversation, count); err != nil {
			return err
		}
	}
	return nil
}

func measure(ctx context.Context, client *http.Client, instance *lsproc.Instance, conversation string, count int) error {
	subscriber := fmt.Sprintf("agy-benchmark-%d", time.Now().UnixNano())
	payload, err := json.Marshal(map[string]any{
		"conversationId": conversation, "subscriberId": subscriber,
		"initialStepsPageBounds":              map[string]int{"startIndex": -count},
		"initialGeneratorMetadatasPageBounds": map[string]int{"startIndex": -1},
		"initialExecutorMetadatasPageBounds":  map[string]int{"startIndex": 0, "endIndexExclusive": 0},
		"trajectoryVerbosity":                 "CLIENT_TRAJECTORY_VERBOSITY_PROD_UI",
	})
	if err != nil {
		return err
	}
	body := make([]byte, 5, len(payload)+5)
	binary.BigEndian.PutUint32(body[1:], uint32(len(payload)))
	body = append(body, payload...)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, instance.BaseURL()+service+"StreamAgentStateUpdates", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/connect+json")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set(lsproc.CSRFHeader, instance.CSRFToken)
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("stream returned %s", resp.Status)
	}
	initial, size, err := readFrame(resp.Body)
	elapsed := time.Since(start)
	if err != nil {
		return err
	}
	steps := initial.Update.MainTrajectoryUpdate.StepsUpdate
	if steps == nil {
		return fmt.Errorf("first frame has no conversation page")
	}
	if len(steps.Indices) != count || steps.Indices[0] != steps.TotalLength-count {
		return fmt.Errorf("initial page does not contain the latest %d steps: got %d indices, total %d, bounds %+v", count, len(steps.Indices), steps.TotalLength, steps.PageBounds)
	}
	fmt.Printf("Latest %2d steps: %7d bytes, first frame %s\n", count, size, elapsed.Round(time.Microsecond))
	if count == 50 {
		return nil
	}
	_, err = instance.Call(ctx, "RequestAgentStatePageUpdate", map[string]any{
		"conversationId": conversation, "subscriberId": subscriber,
		"stepPageBounds": map[string]int{"startIndex": steps.TotalLength - 115},
	})
	if err != nil {
		return err
	}
	for {
		update, _, err := readFrame(resp.Body)
		if err != nil {
			return err
		}
		older := update.Update.MainTrajectoryUpdate.StepsUpdate
		if older == nil || len(older.Indices) == 0 {
			continue
		}
		pageStart := older.PageBounds.StartIndex
		if pageStart < 0 {
			pageStart += older.TotalLength
		}
		if pageStart <= steps.TotalLength-115 && older.Indices[0] < steps.Indices[0] {
			fmt.Println("Scrollback: native pagination expanded the page to at least 115 steps")
			return nil
		}
	}
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
