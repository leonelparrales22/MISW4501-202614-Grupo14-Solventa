// Package cognito creates the accounts of the insured in the Cognito user
// pool (HU-M1.1-01). The pool has allow_admin_create_user_only, so nobody
// signs up against Cognito directly: this service decides who gets an
// account.
package cognito

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	cip "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider/types"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// API is the part of the Cognito client this adapter needs.
type API interface {
	AdminCreateUser(ctx context.Context, in *cip.AdminCreateUserInput, opts ...func(*cip.Options)) (*cip.AdminCreateUserOutput, error)
	AdminSetUserPassword(ctx context.Context, in *cip.AdminSetUserPasswordInput, opts ...func(*cip.Options)) (*cip.AdminSetUserPasswordOutput, error)
	AdminAddUserToGroup(ctx context.Context, in *cip.AdminAddUserToGroupInput, opts ...func(*cip.Options)) (*cip.AdminAddUserToGroupOutput, error)
}

// Provider is the ports.IdentityProvider over a Cognito user pool.
type Provider struct {
	api    API
	poolID string
	group  string
}

var _ ports.IdentityProvider = (*Provider)(nil)

// NewProvider builds a Provider for a user pool. group is the one every
// account created here joins.
func NewProvider(api API, poolID, group string) *Provider {
	return &Provider{api: api, poolID: poolID, group: group}
}

// CreateAccount creates the account of an insured and returns its subject.
//
// MessageAction is SUPPRESS: Cognito would otherwise email a temporary
// password, and the password is the one the person chose. The account is left
// CONFIRMED by setting that password as permanent, so it can be used to sign
// in right away.
//
// It returns ports.ErrAccountExists when the email already has an account.
func (p *Provider) CreateAccount(ctx context.Context, email, password, fullName string) (string, error) {
	created, err := p.api.AdminCreateUser(ctx, &cip.AdminCreateUserInput{
		UserPoolId:    aws.String(p.poolID),
		Username:      aws.String(email),
		MessageAction: types.MessageActionTypeSuppress,
		UserAttributes: []types.AttributeType{
			{Name: aws.String("email"), Value: aws.String(email)},
			{Name: aws.String("email_verified"), Value: aws.String("true")},
			{Name: aws.String("name"), Value: aws.String(fullName)},
		},
	})
	if err != nil {
		var exists *types.UsernameExistsException
		if errors.As(err, &exists) {
			return "", ports.ErrAccountExists
		}
		return "", fmt.Errorf("create the account: %w", err)
	}

	if _, err := p.api.AdminSetUserPassword(ctx, &cip.AdminSetUserPasswordInput{
		UserPoolId: aws.String(p.poolID),
		Username:   aws.String(email),
		Password:   aws.String(password),
		Permanent:  true,
	}); err != nil {
		return "", fmt.Errorf("set the password of the account: %w", err)
	}

	// The group carries the role the token announces in cognito:groups, which
	// is what the services authorize against.
	if _, err := p.api.AdminAddUserToGroup(ctx, &cip.AdminAddUserToGroupInput{
		UserPoolId: aws.String(p.poolID),
		Username:   aws.String(email),
		GroupName:  aws.String(p.group),
	}); err != nil {
		return "", fmt.Errorf("add the account to the group %s: %w", p.group, err)
	}

	return subjectOf(created.User), nil
}

// subjectOf reads the sub of the created user. Cognito returns it among the
// attributes, not as a field of its own.
func subjectOf(user *types.UserType) string {
	if user == nil {
		return ""
	}
	for _, a := range user.Attributes {
		if a.Name != nil && *a.Name == "sub" && a.Value != nil {
			return *a.Value
		}
	}
	return ""
}
