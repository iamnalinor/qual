package runtime

import (
	"encoding/json"
	"fmt"

	"github.com/iamnalinor/qual/internal/domain"
)

const requestProtocolAllowance = 8192

func outputLimit(model domain.ModelInfo) int {
	if model.MaxOutputTokens > 0 {
		return min(8192, model.MaxOutputTokens)
	}
	return 8192
}

func clampOutput(request *domain.ModelRequest, model domain.ModelInfo) {
	limit := outputLimit(model)
	if request.MaxOutputTokens <= 0 || request.MaxOutputTokens > limit {
		request.MaxOutputTokens = limit
	}
}

func requestSize(request domain.ModelRequest) (int, error) {
	data, err := json.Marshal(request)
	if err != nil {
		return 0, fmt.Errorf("measure model context: %w", err)
	}
	return len(data), nil
}

func fitsContext(request domain.ModelRequest, model domain.ModelInfo) (bool, error) {
	if model.ContextWindow <= 0 {
		return true, nil
	}
	size, err := requestSize(request)
	if err != nil {
		return false, err
	}
	// Bytes plus protocol allowance conservatively bound serialized input tokens.
	return size+requestProtocolAllowance+request.MaxOutputTokens <= model.ContextWindow, nil
}

func requireContext(request domain.ModelRequest, model domain.ModelInfo) error {
	fits, err := fitsContext(request, model)
	if err != nil {
		return err
	}
	if !fits {
		return coded(domain.ErrInvalidConfig, "model context cannot fit request contract, tools and output allowance; reduce context or select a larger verified model", nil)
	}
	return nil
}
