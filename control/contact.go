package control

import (
	"fmt"
	"strings"
)

type ContactExport struct {
	Contacts []Contact `json:"contacts"`
}

type Contact struct {
	DisplayName  string   `json:"display_name"`
	PhoneNumbers []string `json:"phone_numbers"`
}

func ValidateContactExport(value ContactExport) error {
	for i, contact := range value.Contacts {
		if strings.TrimSpace(contact.DisplayName) == "" {
			return fmt.Errorf("contact %d display name is required", i)
		}
		if len(contact.PhoneNumbers) == 0 {
			return fmt.Errorf("contact %d requires at least one phone number", i)
		}
		seen := map[string]struct{}{}
		for _, phone := range contact.PhoneNumbers {
			phone = strings.TrimSpace(phone)
			if phone == "" {
				return fmt.Errorf("contact %d contains an empty phone number", i)
			}
			if _, ok := seen[phone]; ok {
				return fmt.Errorf("contact %d contains duplicate phone number %q", i, phone)
			}
			seen[phone] = struct{}{}
		}
	}
	return nil
}
