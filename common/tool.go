package common

import (
	"github.com/google/uuid"
	"strings"
)

func GetUUIDHex() string {
	return strings.ReplaceAll(uuid.New().String(), "-", "")
}
