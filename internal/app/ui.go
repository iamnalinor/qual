package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/iamnalinor/qual/internal/config"
	"github.com/iamnalinor/qual/internal/domain"
	"github.com/iamnalinor/qual/internal/tui"
)

// #nosec G101 -- Translation strings name credential-reference syntax; no credential values are present.
var russianUI = map[string]string{
	"External sandbox":                     "Внешняя изоляция",
	"Action":                               "Действие",
	"Question":                             "Вопрос",
	"Proposed personal settings and rules": "Предлагаемые личные настройки и правила",
	"User settings proposal":               "Предложение пользовательских настроек",
	"Project trust (trusted/untrusted):":   "Доверие к проекту (trusted/untrusted):",
	"Apply these personal settings and rules? (yes/no):": "Применить личные настройки и правила? (yes/no или да/нет):",
	"Activate these settings? (yes/no):":                 "Применить настройки? (yes/no или да/нет):",
	"Reviewer model:":                                    "Модель ревьюера:",
	"Credential reference (env:NAME or profile:NAME; run qual auth login to create a profile):": "Ссылка на учётные данные (env:NAME или profile:NAME; профиль создаётся через qual auth login):",
	"User input unavailable in noninteractive mode":                                             "Ввод пользователя недоступен в неинтерактивном режиме",
	"Acceptance":   "Применение",
	"Budget":       "Бюджет",
	"Execute tool": "Выполнение инструмента", "Tool completed": "Инструмент завершён", "Tool failed": "Ошибка инструмента", "Budget update": "Обновление бюджета", "Role progress": "Состояние роли", "Saving artifact": "Сохранение материала",
	"investigation": "исследование", "finalization": "завершение", "not reported": "не сообщается", "launcher-reported active": "активна по данным запускающего процесса",
	"Error": "Ошибка", "auth": "Ошибка авторизации", "invalid_config": "Неверная конфигурация", "tool": "Ошибка инструмента", "incomplete": "Неполная проверка",
}

func (a *App) text(english string) string {
	if a.UILanguage == "ru" {
		if translated, ok := russianUI[english]; ok {
			return translated
		}
	}
	if a.UILanguage == "ru" {
		if translated, ok := serviceRussian[english]; ok {
			return translated
		}
	}
	if a.UILanguage == "ru" {
		if translated, ok := terminalRussian[english]; ok {
			return translated
		}
	}
	return english
}

func (a *App) selectUI(e config.Effective) error {
	language := e.Settings.UILanguage
	if language == "" {
		language = "en"
	}
	if language != "en" && language != "ru" {
		return &domain.Error{Code: domain.ErrUnsupportedCapability, Message: "UI language supports en or ru"}
	}
	a.UILanguage = language
	return nil
}

func (a *App) globalUI(ctx context.Context) error {
	e, err := a.Config(ctx, "")
	if err != nil {
		return err
	}
	return a.selectUI(e)
}

func (a *App) proposal(ctx context.Context, title, destination string, preview []byte) error {
	if a.terminal != nil {
		_, err := a.terminal.Ask(ctx, tui.Prompt{Title: a.text(title), Description: destination + "\n\n" + string(preview), Options: []tui.Option{{Label: a.text("Continue to confirmation"), Value: "continue"}}})
		return err
	}
	if a.JSON {
		return json.NewEncoder(a.Err).Encode(map[string]any{"schema_version": 1, "type": "proposal", "message": a.text(title), "destination": destination, "preview": string(preview)})
	}
	_, err := fmt.Fprintf(a.Err, "%s (%s):\n%s", a.text(title), destination, preview)
	return err
}

func (a *App) acceptance(accepted bool) error {
	if a.JSON {
		return json.NewEncoder(a.Err).Encode(map[string]any{"schema_version": 1, "type": "acceptance", "message": a.text("Acceptance"), "accepted": accepted})
	}
	return nil
}

func (a *App) question(question string) error {
	if a.JSON {
		return json.NewEncoder(a.Err).Encode(map[string]any{"schema_version": 1, "type": "question", "ui_language": a.UILanguage, "message": a.text("Question"), "question": a.text(question), "available": a.terminal != nil})
	}
	_, err := fmt.Fprintf(a.Err, "%s: %s\n", a.text("Question"), a.text(question))
	return err
}

func uiGuidance(language string) string {
	if language == "ru" {
		return "\nAsk user questions in Russian (ru), independently of the report language. Keep the configured output language for reports and findings."
	}
	return "\nAsk user questions in English (en), independently of the report language. Keep the configured output language for reports and findings."
}

// InteractionError adds a localized diagnostic heading while preserving stable codes and causes.
func (a *App) InteractionError(err error) error {
	if err == nil || a.UILanguage != "ru" {
		return err
	}
	var coded *domain.Error
	heading := a.text("Error")
	if errors.As(err, &coded) {
		heading = a.text(string(coded.Code))
	}
	return fmt.Errorf("%s: %w", heading, err)
}
