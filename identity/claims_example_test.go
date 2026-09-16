package identity_test

import (
	"encoding/json/jsontext"
	"fmt"

	"github.com/asteroid-computing/go-lambda-edge/identity"
)

func ExampleParseClaims() {
	claims, err := identity.ParseClaims(jsontext.Value(`{"tenant":"orders","sequence":9007199254740993}`))
	if err != nil {
		fmt.Println(err)
		return
	}
	if claim, present := claims.Lookup("sequence"); present {
		if number, exact := claim.NumberText(); exact {
			fmt.Println(number)
		}
	}
	// Output: 9007199254740993
}

func ExampleNewTextClaims() {
	claims, err := identity.NewTextClaims(map[string]string{"cognito:groups": `["staff"]`})
	if err != nil {
		fmt.Println(err)
		return
	}
	claim, present := claims.Lookup("cognito:groups")
	_, array := claim.Array()
	fmt.Println("present:", present, "array:", array)
	// Output: present: true array: false
}
