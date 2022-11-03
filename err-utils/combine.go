package errUtils

import "fmt"

func CombineErrors(header string, errors []error) error {
	if len(errors) == 0 {
		return nil
	}

	outputErr := fmt.Errorf(header)
	for _, err := range errors {
		outputErr = fmt.Errorf("%w; %v", outputErr, err.Error())
	}

	return outputErr
}
