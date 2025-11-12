package utils

import (
	"fmt"
	"os"
)

func EnsureUploadsDir() error {
	if _, err := os.Stat("uploads"); os.IsNotExist(err) {
		err := os.Mkdir("uploads", 0755)
		if err != nil {
			return fmt.Errorf("failed to create uploads directory: %v", err)
		}
		fmt.Println("Created uploads directory")
	}
	return nil
}
