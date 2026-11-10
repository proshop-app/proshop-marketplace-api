package main

import (
	app "github.com/proshop-app/proshop-marketplace/internal/application/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func validRemoveHintOptions(operation string, command app.RemoveHintCommand) bool {
	if operation != "remove-hint" {
		return command.Attribute == "" && command.Dictionary == "" && command.PreviousVersion == 0
	}
	return taxonomy.ValidCode(command.Attribute) && taxonomy.ValidCode(command.Dictionary) &&
		command.PreviousVersion > 0 && command.PreviousVersion < command.ExpectedVersion &&
		command.ExpectedVersion <= taxonomy.MaxTaxonomyVersion
}
