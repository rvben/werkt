package authoring

type operationSpec struct {
	ID          string
	Title       string
	Description string
	Connector   string
	Operation   string
	Effect      string
	Stage       int
	Secrets     []SecretBinding
	Egress      []EgressRule
}

var operationCatalog = []operationSpec{
	{
		ID: "zoom-recording", Title: "Read a Zoom recording", Description: "Fetch Zoom cloud-recording metadata for a supplied recording UUID", Connector: "zoom", Operation: "recording", Effect: "read", Stage: 10,
		Secrets: []SecretBinding{
			{Environment: "ZOOM_ACCOUNT_ID", Secret: "zoom/account-id", Description: "Zoom Server-to-Server OAuth account ID"},
			{Environment: "ZOOM_CLIENT_ID", Secret: "zoom/client-id", Description: "Zoom Server-to-Server OAuth client ID"},
			{Environment: "ZOOM_CLIENT_SECRET", Secret: "zoom/client-secret", Description: "Zoom Server-to-Server OAuth client secret"},
		},
		Egress: []EgressRule{{Host: "zoom.us", Port: 443}, {Host: "api.zoom.us", Port: 443}},
	},
	{
		ID: "sheets-read", Title: "Read Google Sheets values", Description: "Read values from a supplied Google spreadsheet ID and range", Connector: "google-sheets", Operation: "values", Effect: "read", Stage: 10,
		Secrets: []SecretBinding{{Environment: "GOOGLE_SERVICE_ACCOUNT_JSON", Secret: "google/service-account", Description: "Google service-account JSON"}},
		Egress:  []EgressRule{{Host: "oauth2.googleapis.com", Port: 443}, {Host: "sheets.googleapis.com", Port: 443}},
	},
	{
		ID: "openai-transcribe", Title: "Transcribe audio", Description: "Transcribe an audio file supplied to the run using OpenAI", Connector: "openai", Operation: "transcribe", Effect: "compute", Stage: 20,
		Secrets: []SecretBinding{{Environment: "OPENAI_API_KEY", Secret: "openai/api-key", Description: "OpenAI API key"}}, Egress: []EgressRule{{Host: "api.openai.com", Port: 443}},
	},
	{
		ID: "openai-vision", Title: "Analyze an image", Description: "Analyze an image supplied to the run using an OpenAI vision model", Connector: "openai", Operation: "vision", Effect: "compute", Stage: 20,
		Secrets: []SecretBinding{{Environment: "OPENAI_API_KEY", Secret: "openai/api-key", Description: "OpenAI API key"}}, Egress: []EgressRule{{Host: "api.openai.com", Port: 443}},
	},
	{
		ID: "openai-chat", Title: "Transform or summarize content", Description: "Transform, classify, summarize, or draft text using an OpenAI chat model", Connector: "openai", Operation: "chat", Effect: "compute", Stage: 30,
		Secrets: []SecretBinding{{Environment: "OPENAI_API_KEY", Secret: "openai/api-key", Description: "OpenAI API key"}}, Egress: []EgressRule{{Host: "api.openai.com", Port: 443}},
	},
	{ID: "operator-notify", Title: "Notify the operator", Description: "Queue an audited notification in the Werkt workspace for an operator", Connector: "werkt", Operation: "notify", Effect: "write", Stage: 40},
	{
		ID: "sheets-update", Title: "Update Google Sheets values", Description: "Write computed values to a supplied Google spreadsheet ID and range", Connector: "google-sheets", Operation: "update", Effect: "write", Stage: 40,
		Secrets: []SecretBinding{{Environment: "GOOGLE_SERVICE_ACCOUNT_JSON", Secret: "google/service-account", Description: "Google service-account JSON"}},
		Egress:  []EgressRule{{Host: "oauth2.googleapis.com", Port: 443}, {Host: "sheets.googleapis.com", Port: 443}},
	},
	{
		ID: "sheets-clear", Title: "Clear a Google Sheets range", Description: "Destructively clear values from a supplied Google spreadsheet ID and range", Connector: "google-sheets", Operation: "clear", Effect: "write", Stage: 40,
		Secrets: []SecretBinding{{Environment: "GOOGLE_SERVICE_ACCOUNT_JSON", Secret: "google/service-account", Description: "Google service-account JSON"}},
		Egress:  []EgressRule{{Host: "oauth2.googleapis.com", Port: 443}, {Host: "sheets.googleapis.com", Port: 443}},
	},
}

func operationByID(id string) (operationSpec, bool) {
	for _, operation := range operationCatalog {
		if operation.ID == id {
			return operation, true
		}
	}
	return operationSpec{}, false
}
