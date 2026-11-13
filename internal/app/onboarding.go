package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/iamnalinor/qual/internal/auth"
	"github.com/iamnalinor/qual/internal/config"
	"github.com/iamnalinor/qual/internal/domain"
	"github.com/iamnalinor/qual/internal/provider"
	"github.com/iamnalinor/qual/internal/tui"
)

func (a *App) providerChoice(ctx context.Context) (string, error) {
	return a.choose(ctx, "How would you like to connect?", "Choose the provider and billing route you already use.", []tui.Option{
		{Label: "ChatGPT account", Description: "Sign in through your browser. Requires an enabled token-sharing OAuth application; no API-key fallback.", Value: "chatgpt"},
		{Label: "OpenAI API", Description: "Use an API key billed to your OpenAI account.", Value: "openai"},
		{Label: "Anthropic API", Description: "Use a Claude API key.", Value: "anthropic"},
		{Label: "Google Gemini API", Description: "Use a Gemini API key.", Value: "gemini"},
		{Label: "OpenRouter", Description: "Choose a model through your OpenRouter account.", Value: "openrouter"},
		{Label: "OpenAI-compatible server", Description: "Connect your own trusted endpoint. Model capabilities must be configured explicitly.", Value: "openai-compatible"},
		{Label: "Legacy Codex account", Description: "Explicit compatibility route, separate from ChatGPT token sharing.", Value: "codex"},
		{Label: "Self-managed GitLab (repository access)", Description: "Trust a GitLab host and connect its host-specific token. Model defaults stay unchanged.", Value: "gitlab-host"},
	})
}

func (a *App) guidedInit(ctx context.Context, o InitOptions) (InitOptions, error) {
	var err error
	o, err = a.initAccount(ctx, o)
	if err != nil {
		return o, err
	}
	o, err = a.initModels(ctx, o)
	if err != nil {
		return o, err
	}
	o, err = a.initLanguages(ctx, o)
	if err != nil {
		return o, err
	}
	return a.initDefaults(ctx, o)
}

func (a *App) initAccount(ctx context.Context, o InitOptions) (InitOptions, error) {
	var err error
	if o.Kind == "" {
		o.Kind, err = a.providerChoice(ctx)
		if err != nil {
			return o, err
		}
	}

	if o.Kind == "openai-compatible" && o.Endpoint == "" {
		o.Endpoint, err = a.terminal.Ask(ctx, tui.Prompt{Title: a.text("Server URL"), Description: a.text("Use a trusted API endpoint, including its /v1 path.")})
		if err != nil {
			return o, err
		}
	}
	if o.Credential == "" {
		o.Credential, err = a.accountChoice(ctx, o.Kind, o.Device)
		if err != nil {
			return o, err
		}
	}
	if o.Connection == "" {
		o.Connection, err = a.initConnection(ctx, o)
		if err != nil {
			return o, err
		}
	}
	return o, nil
}

func (a *App) initModels(ctx context.Context, o InitOptions) (InitOptions, error) {
	var err error
	if o.Model == "" {
		o.Model, err = a.modelChoice(ctx, o)
		if err != nil {
			return o, err
		}
	}
	if o.LeaderModel == "" {
		selected, e := a.choose(ctx, "Who should verify the findings?", "The leader checks the review independently before the final result.", []tui.Option{{Label: a.text("Model") + ": " + o.Model, Description: "Separate reviewer and leader runs using the same model.", Value: "same"}, {Label: a.text("Choose another model"), Value: "other"}})
		if e != nil {
			return o, e
		}
		o.LeaderModel = o.Model
		if selected == "other" {
			o.LeaderModel, err = a.modelChoice(ctx, o)
			if err != nil {
				return o, err
			}
		}
	}
	if o.Soft == "" && o.Hard == "" {
		o.Soft, o.Hard, err = a.budgetChoice(ctx, modelPricingKnown(o))
		if err != nil {
			return o, err
		}
	}
	return o, nil
}

func (a *App) initLanguages(ctx context.Context, o InitOptions) (InitOptions, error) {
	var err error
	o.UILanguage, err = a.choose(ctx, "Interface language", "Reports can use a different language.", []tui.Option{{Label: "English", Value: "en"}, {Label: "Русский", Value: "ru"}})
	if err != nil {
		return o, err
	}
	o.ReviewLanguage, err = a.choose(ctx, "Report language", "Language for findings and the final report.", []tui.Option{{Label: "English", Value: "en"}, {Label: "Русский", Value: "ru"}})
	return o, err
}

func (a *App) accountChoice(ctx context.Context, kind string, device bool) (string, error) {
	service, err := a.authService()
	if err != nil {
		return "", err
	}
	profiles, err := service.List(ctx)
	if err != nil {
		return "", err
	}
	options, err := a.accountOptions(ctx, kind, profiles)
	if err != nil {
		return "", err
	}
	reserved, err := a.reservedAccountIDs(ctx, profiles)
	if err != nil {
		return "", err
	}
	id := newAccountOptions(kind, reserved, device).Profile
	if len(options) > 0 {
		options = append(options, tui.Option{Label: a.text("Connect another account"), Value: "new"})
		choice, e := a.choose(ctx, "Which account?", "Use a saved account, or sign in again to repair its existing credential reference.", options)
		if e != nil {
			return "", e
		}
		if selected, ok := strings.CutPrefix(choice, "use/"); ok {
			return "profile:" + selected, nil
		}
		if selected, ok := strings.CutPrefix(choice, "reconnect/"); ok {
			id = selected
		}
	}
	id, err = a.connectAccount(ctx, service, LoginOptions{Kind: kind, Profile: id, Device: device})
	if err != nil {
		return "", err
	}
	return "profile:" + id, nil
}

func uniqueProfile(kind string, profiles []auth.Profile) string {
	used := map[string]bool{}
	for _, profile := range profiles {
		used[profile.ID] = true
	}
	id := kind
	for n := 2; used[id]; n++ {
		id = kind + "-" + strconv.Itoa(n)
	}
	return id
}

func (a *App) modelChoice(ctx context.Context, o InitOptions) (string, error) {
	p := lazyProvider{app: a, connection: config.Connection{Kind: o.Kind, Endpoint: o.Endpoint, Credential: o.Credential}}
	a.terminal.Activity(tui.Activity{Summary: a.text("Loading model information…")})
	models, err := p.Models(ctx)
	if err != nil {
		return "", err
	}
	var options []tui.Option
	for _, model := range models {
		if !model.Tools && o.Kind != "chatgpt" {
			continue
		}
		options = append(options, tui.Option{Label: model.ID, Description: a.localizedModelDescription(model), Value: model.ID})
	}
	options = append(options, tui.Option{Label: a.text("Enter a model ID"), Description: a.text("The account model list will be checked before activation. Unknown models need explicit capability settings."), Value: "manual"})
	model, err := a.choose(ctx, "Choose the reviewer model", "Choose a model. Prices are per million tokens (input / output); availability is checked before saving.", options)
	if err != nil {
		return "", err
	}
	if model == "manual" {
		return a.terminal.Ask(ctx, tui.Prompt{Title: a.text("Model ID"), Description: a.text("Use the exact model identifier from your provider.")})
	}
	a.terminal.Activity(tui.Activity{Summary: a.text("Model") + ": " + model})
	return model, nil
}

func modelDescription(model domain.ModelInfo) string {
	parts := []string{model.DisplayName}
	if model.ContextWindow > 0 {
		parts = append(parts, fmt.Sprintf("%dk context", model.ContextWindow/1000))
	}
	parts = append(parts, "USD / million tokens: "+modelPrice(model.Pricing))
	return strings.Join(parts, " · ")
}

func (a *App) budgetChoice(ctx context.Context, known bool) (string, string, error) {
	if !known {
		_, err := a.choose(ctx, "Spending is not reported in USD", "This model's pricing is unknown. Qual cannot enforce a USD ceiling; your provider account limits still apply.", []tui.Option{{Label: a.text("Use provider account limits"), Value: "none"}})
		return "", "", err
	}
	value, err := a.choose(ctx, "Set a spending limit", "Thresholds stop new requests using recorded usage and estimates. Final provider charges may exceed estimates.", []tui.Option{
		{Label: "$1 per role", Description: "Reviewer and leader each stop near $1. Soft threshold $0.50; provider charges depend on actual usage.", Value: "small"},
		{Label: "$5 per role", Description: "Reviewer and leader each stop near $5. Soft threshold $2.50; provider charges depend on actual usage.", Value: "medium"},
		{Label: a.text("No USD limit"), Description: a.text("Use the provider's account limits. Spending may be unknown for subscription routes."), Value: "none"},
	})
	switch value {
	case "small":
		return "0.50", "1", err
	case "medium":
		return "2.50", "5", err
	default:
		return "", "", err
	}
}

func (a *App) initDefaults(ctx context.Context, o InitOptions) (InitOptions, error) {
	existing, err := a.Config(ctx, "")
	if err != nil {
		return o, err
	}
	if len(existing.Agents) == 0 {
		return o, nil
	}
	use, err := a.choose(ctx, "Use these models as your review defaults?", "Existing provider accounts and project settings will be kept.", []tui.Option{{Label: a.text("Keep current review defaults"), Description: a.text("Add this account for projects that require it."), Value: "keep"}, {Label: a.text("Use these review defaults"), Value: "replace"}})
	o.AddOnly = use == "keep"
	return o, err
}

func modelPricingKnown(o InitOptions) bool {
	if oauthKind(o.Kind) || len(o.Capabilities) > 0 {
		return false
	}
	for _, id := range []string{o.Model, defaultModel(o.LeaderModel, o.Model), defaultModel(o.SetupModel, o.Model)} {
		if !catalogPricingKnown(o.Kind, id) {
			return false
		}
	}
	return true
}

func catalogPricingKnown(kind, id string) bool {
	for _, model := range provider.Catalog() {
		if model.ID == id && model.Provider == providerKind(kind) {
			return model.Pricing.Known
		}
	}
	return false
}

func (a *App) localizedModelDescription(model domain.ModelInfo) string {
	text := modelDescription(model)
	for _, word := range []string{"context", "USD / million tokens", "unknown"} {
		text = strings.ReplaceAll(text, word, a.text(word))
	}
	return text
}
