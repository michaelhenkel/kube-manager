/*
Copyright 2020 Juniper Networks.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
package mcmanager

import "fmt"

// A top-level multi-cluster error that contains errors thrown from either the
// config manager or the managed cluster manager.
type MultiClusterError struct {
	msg                 string
	ConfigClusterError  error
	ManagedClusterError error
}

// Error interface implementation
func (e *MultiClusterError) Error() string {
	return e.msg
}

// Package-level message formatter for `MultiClusterError`
func fmtError(configError error, managedError error) *MultiClusterError {
	if configError == nil && managedError == nil {
		return nil
	}
	var msg string
	if configError == nil {
		msg = fmt.Sprintf(
			"there was an error on the multi-cluster manager: %v", managedError)
	} else if managedError == nil {
		msg = fmt.Sprintf(
			"there was an error on the config manager: %v", configError)
	} else {
		msg = fmt.Sprintf(
			"there were errors on both the config manager and the multi-cluster manager:\n"+
				"config error: %v\nmanaged error: %v", configError, managedError)
	}
	e := &MultiClusterError{
		msg:                 msg,
		ConfigClusterError:  configError,
		ManagedClusterError: managedError,
	}
	return e
}
