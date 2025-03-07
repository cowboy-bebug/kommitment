package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/cowboy-bebug/kommit/internal/utils"
	"github.com/invopop/jsonschema"
	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
)

// OpenAI client configuration
const (
	timeout          = 10 * time.Second
	temperature      = 0.0
	topP             = 1.0
	presencePenalty  = 0.0
	frequencyPenalty = 0.0
)

// System prompts
const (
	kommitSystemPrompt = "You are an AI that generates Conventional Git commit messages."
	jsonResponsePrompt = "Return your response as a valid JSON object."
)

// User prompts
const (
	promptMain = "Generate a single commit message following the **Conventional Commit** format, adhering to these rules:\n"

	promptGeneralRules = `
## **General Rules**
- Do **not**:
  - Wrap the message in a code block or triple backticks.
  - Use ` + "`" + "build" + "`" + ` as a scope.
  - Suggest ` + "`" + "feat" + "`" + ` for build scripts.
  - Include comments or remarks.

- **Do**:
	- Try your best to guess what the git diff is about.
`

	promptCommitTypeGuidelines = `
## **Commit Type Guidelines**
- Use **lowercase** commit types:
  - ` + "`" + "build" + "`" + `: For build systems, scripts, or settings (e.g., Makefile, Dockerfile).
  - ` + "`" + "docs" + "`" + `: For documentation changes (e.g., README, CHANGELOG), **but not** script or code changes.
`

	promptScopeRules = `
## **Scope Rules**
- Use the **module or package name** as the scope.
- Leave the scope **empty** if:
  - The changes are **not** tied to a specific module or package.
  - The changes span **multiple modules, packages, files or scopes**.
`

	promptMessageFormatting = `
## **Message Formatting**
- **Subject**:
  - Use **imperative mood** (present tense).
- **Body**:
  - Use **bullet points**.
  - Use **imperative mood** (present tense).
  - Capitalize the **first letter** of each bullet point.
  - Wrap lines at **72 characters**.
  - Include a body **only if** the changes are significant.
`

	kommitBaseUserPrompt = promptMain + promptGeneralRules + promptCommitTypeGuidelines + promptScopeRules + promptMessageFormatting
)

func newClient() (*openai.Client, error) {
	// KOMMIT_API_KEY takes precedence
	apiKey := os.Getenv("KOMMIT_API_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("OPENAI_API_KEY")
	}

	// If both are missing, return an error
	if apiKey == "" {
		return nil, &APIKeyMissingError{}
	}

	return openai.NewClient(
		option.WithAPIKey(apiKey),
		option.WithRequestTimeout(timeout),
	), nil
}

func chat(model, prompt string) (string, error) {
	client, err := newClient()
	if err != nil {
		return "", err
	}

	resp, err := client.Chat.Completions.New(context.TODO(), openai.ChatCompletionNewParams{
		Model: openai.F(model),
		Messages: openai.F([]openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(kommitSystemPrompt),
			openai.UserMessage(prompt),
		}),
		Temperature:      openai.Float(temperature),
		TopP:             openai.Float(topP),
		PresencePenalty:  openai.Float(presencePenalty),
		FrequencyPenalty: openai.Float(frequencyPenalty),
	})
	if err != nil {
		return "", &OpenAIRequestError{Err: err}
	}

	return resp.Choices[0].Message.Content, nil
}

func GenerateSchema[T any]() any {
	reflector := jsonschema.Reflector{
		AllowAdditionalProperties: false,
		DoNotReference:            true,
	}
	var v T
	schema := reflector.Reflect(v)
	return schema
}

func chatStructured[T any](model, prompt string, schema openai.ResponseFormatJSONSchemaJSONSchemaParam) (T, error) {
	client, err := newClient()
	if err != nil {
		var empty T
		return empty, err
	}

	resp, err := client.Chat.Completions.New(context.TODO(), openai.ChatCompletionNewParams{
		Model:            openai.F(model),
		Temperature:      openai.Float(temperature),
		TopP:             openai.Float(topP),
		PresencePenalty:  openai.Float(presencePenalty),
		FrequencyPenalty: openai.Float(frequencyPenalty),
		Messages: openai.F([]openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(kommitSystemPrompt + jsonResponsePrompt),
			openai.UserMessage(prompt),
		}),
		ResponseFormat: openai.F[openai.ChatCompletionNewParamsResponseFormatUnion](
			openai.ResponseFormatJSONSchemaParam{
				Type:       openai.F(openai.ResponseFormatJSONSchemaTypeJSONSchema),
				JSONSchema: openai.F(schema),
			}),
	})
	if err != nil {
		var empty T
		return empty, &OpenAIRequestError{Err: err}
	}

	content := resp.Choices[0].Message.Content
	var result T
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		var empty T
		return empty, &JSONParseError{Err: err}
	}

	return result, nil
}

func GenerateCommitMessage(config *utils.Config, diff string) (string, error) {
	prompt := kommitBaseUserPrompt

	// context
	prompt += "\n## Context:\n"
	prompt += "- Allowed commit types:\n"
	for _, t := range config.Commit.Types {
		prompt += fmt.Sprintf("  - `%s`\n", t)
	}
	prompt += "- Allowed scopes **(if applicable)**:\n"
	for _, s := range config.Commit.Scopes {
		prompt += fmt.Sprintf("  - `%s`\n", s)
	}

	// diff
	prompt += "## Git Diff:\n"
	prompt += "**Based on the following diff**:\n"
	prompt += "```diff\n"
	prompt += diff + "\n"
	prompt += "```\n"

	return chat(config.LLM.Model, prompt)
}

type Scopes struct {
	Scopes []string `json:"scopes"`
}

var StructuredScopesSchema = GenerateSchema[Scopes]()

func GenerateScopesFromFilenames(model string, filenames, existingScopes []string) ([]string, error) {
	prompt := "Based on the following project structure, guess module or package names used in this project:\n"
	prompt += strings.Join(filenames, "\n")

	prompt += "Here are some existing scopes:\n"
	prompt += strings.Join(existingScopes, "\n")

	prompt += "\n\n"
	prompt += "- Do not suggest nested names\n"
	prompt += "- Do not suggest names with \"/\""
	prompt += "- Do not suggest docs as a scope"

	schemaParam := openai.ResponseFormatJSONSchemaJSONSchemaParam{
		Name:        openai.F("names"),
		Description: openai.F("A list of module or package names."),
		Schema:      openai.F(StructuredScopesSchema),
		Strict:      openai.Bool(true),
	}

	result, err := chatStructured[Scopes](model, prompt, schemaParam)
	if err != nil {
		return nil, err
	}

	return result.Scopes, nil
}
