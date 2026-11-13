package app

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/iamnalinor/qual/internal/budget"
	"github.com/iamnalinor/qual/internal/domain"
	"github.com/iamnalinor/qual/internal/provider"
)

func (a *App) renderModels(models []domain.ModelInfo, verified bool) error {
	if a.JSON {
		return a.Output(map[string]any{"catalog_version": provider.CatalogVersion, "models": models, "entitlement_verified": verified})
	}
	if err := a.heading("Models"); err != nil {
		return err
	}
	rows := make([]tableRow, 0, len(models))
	for _, model := range models {
		rows = append(rows, tableRow{model.ID, model.Provider, fmt.Sprintf("%d / %d", model.ContextWindow, model.MaxOutputTokens), strconv.FormatBool(model.Tools), strings.Join(model.ReasoningLevels, ", "), a.text(modelPrice(model.Pricing))})
	}
	if err := a.table(tableRow{a.text("MODEL"), a.text("PROVIDER"), a.text("CONTEXT / OUTPUT"), a.text("TOOLS"), a.text("REASONING"), a.text("USD / MILLION TOKENS (IN / OUT)")}, rows); err != nil {
		return err
	}
	if verified {
		return a.line("\n%s\n", a.text("Availability verified by the selected account."))
	}
	return a.line("\n%s\n", a.text("Catalog capabilities and prices; account availability is not verified. Context pricing tiers may apply."))
}

func modelPrice(price domain.Pricing) string {
	if !price.Known {
		return "unknown"
	}
	format := func(value domain.Money) string {
		return strings.TrimRight(strings.TrimRight(budget.Format(value), "0"), ".")
	}
	return "$" + format(price.InputPerMillion) + " / $" + format(price.OutputPerMillion)
}
