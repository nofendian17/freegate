package domain

type IPRotator interface {
	NewIP() error
	ForceNewIP() error
	CurrentIP() string
}
