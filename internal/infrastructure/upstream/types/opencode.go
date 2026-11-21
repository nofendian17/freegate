package types

type OpenCodeModelList struct {
	Object string          `json:"object"`
	Data   []OpenCodeModel `json:"data"`
}

type OpenCodeModel struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
	Limit   struct {
		Context int64 `json:"context"`
		Input   int64 `json:"input"`
		Output  int64 `json:"output"`
	} `json:"limit"`
}
