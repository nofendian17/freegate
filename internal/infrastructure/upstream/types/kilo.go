package types

type KiloModelList struct {
	Object string      `json:"object"`
	Data   []KiloModel `json:"data"`
}

type KiloModel struct {
	ID            string      `json:"id"`
	Name          string      `json:"name"`
	Created       int64       `json:"created"`
	Description   string      `json:"description"`
	ContextLength int         `json:"context_length"`
	Pricing       KiloPricing `json:"pricing"`
	IsFree        bool        `json:"isFree"`
}

type KiloPricing struct {
	Prompt     string `json:"prompt"`
	Completion string `json:"completion"`
}
