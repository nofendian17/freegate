package types

type OpenCodeModelList struct {
	Object string         `json:"object"`
	Data   []OpenCodeModel `json:"data"`
}

type OpenCodeModel struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
	Cost    string `json:"cost"`
}
