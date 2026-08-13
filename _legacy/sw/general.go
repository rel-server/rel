package sw

import (
	"fmt"
	"log"
)

func Error(msg string, err error) error {
	err = fmt.Errorf("%s: %w", msg, err)
	// LogError(err)
	return err
}

func LogInfo(msg ...interface{}) {
	msgs := append([]interface{}{green("* ")}, msg...)
	log.Print(msgs...)
}

func LogError(msg ...interface{}) {
	msgs := append([]interface{}{red("error ")}, msg...)
	log.Print(msgs...)
}

func LogWarn(msg ...interface{}) {
	msgs := append([]interface{}{yellow("? ")}, msg...)
	log.Print(msgs...)
}
