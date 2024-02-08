/*
Copyright 2022 Red Hat

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

package v1beta1

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/openstack-k8s-operators/lib-common/modules/common/helper"
	"github.com/openstack-k8s-operators/lib-common/modules/common/secret"
	"github.com/openstack-k8s-operators/lib-common/modules/common/service"
	"github.com/openstack-k8s-operators/lib-common/modules/common/util"

	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// NewDatabase returns an initialized DB.
// legacy; should use NewDatabaseForAccount
func NewDatabase(
	databaseName string,
	databaseUser string,
	secret string,
	labels map[string]string,
) *Database {
	return &Database{
		databaseName: databaseName,
		databaseUser: databaseUser,
		secret:       secret,
		labels:       labels,
		name:         "",
		accountName:  "",
		namespace:    "",
	}
}

// NewDatabaseWithNamespace returns an initialized DB.
// legacy; should use NewDatabaseForAccount
func NewDatabaseWithNamespace(
	databaseName string,
	databaseUser string,
	secret string,
	labels map[string]string,
	name string,
	namespace string,
) *Database {
	return &Database{
		databaseName: databaseName,
		databaseUser: databaseUser,
		secret:       secret,
		labels:       labels,
		name:         name,
		accountName:  "",
		namespace:    namespace,
	}
}

// NewDatabaseWithNamespace returns an initialized DB.
func NewDatabaseForAccount(
	databaseInstanceName string,
	databaseName string,
	name string,
	accountName string,
	namespace string,
) *Database {
	return &Database{
		databaseName: databaseName,
		labels: map[string]string{
			"dbName": databaseInstanceName,
		},
		name:        name,
		accountName: accountName,
		namespace:   namespace,
	}
}

// setDatabaseHostname - set the service name of the DB as the databaseHostname
// by looking up the Service via the name of the MariaDB CR which provides it.
func (d *Database) setDatabaseHostname(
	ctx context.Context,
	h *helper.Helper,
	name string,
) error {

	// When the MariaDB CR provides the Service it sets the "cr" label of the
	// Service to "mariadb-<name of the MariaDB CR>". So we use this label
	// to select the right Service. See:
	// https://github.com/openstack-k8s-operators/mariadb-operator/blob/5781b0cf1087d7d28fa285bd5c44689acba92183/pkg/service.go#L17
	// https://github.com/openstack-k8s-operators/mariadb-operator/blob/590ffdc5ad86fe653f9cd8a7102bb76dfe2e36d1/pkg/utils.go#L4
	selector := map[string]string{
		"app": "mariadb",
		"cr":  fmt.Sprintf("mariadb-%s", name),
	}
	serviceList, err := service.GetServicesListWithLabel(
		ctx,
		h,
		h.GetBeforeObject().GetNamespace(),
		selector,
	)
	if err != nil || len(serviceList.Items) == 0 {
		return fmt.Errorf("Error getting the DB service using label %v: %w",
			selector, err)
	}

	// We assume here that a MariaDB CR instance always creates a single
	// Service. If multiple DB services are used the they are managed via
	// separate MariaDB CRs.
	if len(serviceList.Items) > 1 {
		return util.WrapErrorForObject(
			fmt.Sprintf("more then one DB service found %d", len(serviceList.Items)),
			d.database,
			err,
		)
	}
	svc := serviceList.Items[0]
	d.databaseHostname = svc.GetName() + "." + svc.GetNamespace() + ".svc"

	return nil
}

// GetDatabaseHostname - returns the DB hostname which host the DB
func (d *Database) GetDatabaseHostname() string {
	return d.databaseHostname
}

// GetDatabase - returns the DB
func (d *Database) GetDatabase() *MariaDBDatabase {
	return d.database
}

// GetAccount - returns the account
func (d *Database) GetAccount() *MariaDBAccount {
	return d.account
}

// CreateOrPatchDB - create or patch the service DB instance
// Deprecated. Use CreateOrPatchDBByName instead. If you want to use the
// default the DB service instance of the deployment then pass "openstack" as
// the name.
func (d *Database) CreateOrPatchDB(
	ctx context.Context,
	h *helper.Helper,
) (ctrl.Result, error) {
	return d.CreateOrPatchDBByName(ctx, h, "openstack")
}

// CreateOrPatchDBByName - create or patch the service DB instance on
// the DB service. The DB service is selected by the name of the MariaDB CR
// providing the service.
func (d *Database) CreateOrPatchDBByName(
	ctx context.Context,
	h *helper.Helper,
	name string,
) (ctrl.Result, error) {

	if d.name == "" {
		d.name = h.GetBeforeObject().GetName()
	}
	if d.namespace == "" {
		d.namespace = h.GetBeforeObject().GetNamespace()
	}

	db := d.database
	if db == nil {
		// MariaDBDatabase not present; create one to be patched/created

		db = &MariaDBDatabase{
			ObjectMeta: metav1.ObjectMeta{
				Name:      d.name,
				Namespace: d.namespace,
			},
			Spec: MariaDBDatabaseSpec{
				// the DB name must not change, therefore specify it outside the mutuate function
				Name: d.databaseName,
			},
		}
	}

	account := d.account

	if account == nil {
		// MariaDBAccount not present

		accountName := d.accountName
		if accountName == "" {
			// no accountName at all.  this indicates this Database came about
			// using either NewDatabase or NewDatabaseWithNamespace; both
			// legacy and both pass along a databaseUser and secret.

			//, so for forwards compatibility,
			// make a name and a MariaDBAccount for it.  name it the same as
			// the MariaDBDatabase so we can get it back
			// again based on that name alone (also for backwards compatibility).
			accountName = d.name
			d.accountName = accountName
		}

		account = &MariaDBAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      accountName,
				Namespace: d.namespace,
				Labels: map[string]string{
					"mariaDBDatabaseName": d.name,
				},
			},
		}

		// databaseUser was given, this is from legacy mode.  populate it
		// into the account
		if d.databaseUser != "" {
			account.Spec.UserName = d.databaseUser
		}

		// secret was given, this is also from legacy mode.  populate it
		// into the account.  note here that this is osp-secret, which has
		// many PW fields in it.  By setting it here, as was the case when
		// osp-secret was associated directly with MariaDBDatabase, the
		// mariadb-controller is going to use the DatabasePassword value
		// for the password, and **not** any of the controller-specific
		// passwords.
		if d.secret != "" {
			account.Spec.Secret = d.secret
		}
	}

	// set the database hostname on the db instance
	err := d.setDatabaseHostname(ctx, h, name)
	if err != nil {
		return ctrl.Result{}, err
	}

	op, err := controllerutil.CreateOrPatch(ctx, h.GetClient(), db, func() error {
		db.Labels = util.MergeStringMaps(
			db.GetLabels(),
			d.labels,
		)

		err := controllerutil.SetControllerReference(h.GetBeforeObject(), db, h.GetScheme())
		if err != nil {
			return err
		}

		// If the service object doesn't have our finalizer, add it.
		controllerutil.AddFinalizer(db, h.GetFinalizer())

		return nil
	})

	if err != nil && !k8s_errors.IsNotFound(err) {
		return ctrl.Result{}, util.WrapErrorForObject(
			fmt.Sprintf("Error create or update DB object %s", db.Name),
			db,
			err,
		)
	}

	if op != controllerutil.OperationResultNone {
		util.LogForObject(h, fmt.Sprintf("DB object %s created or patched", db.Name), db)
		return ctrl.Result{RequeueAfter: time.Second * 5}, nil
	}

	op_acc, err_acc := CreateOrPatchAccount(
		ctx, h, account,
		map[string]string{
			"mariaDBDatabaseName": d.name,
		},
	)

	if err_acc != nil && !k8s_errors.IsNotFound(err_acc) {
		return ctrl.Result{}, util.WrapErrorForObject(
			fmt.Sprintf("Error create or update account object %s", account.Name),
			account,
			err_acc,
		)
	}

	if op_acc != controllerutil.OperationResultNone {
		util.LogForObject(h, fmt.Sprintf("Account object %s created or patched", account.Name), account)
		return ctrl.Result{RequeueAfter: time.Second * 5}, nil
	}

	err = d.getDBWithName(
		ctx,
		h,
	)
	if err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// WaitForDBCreatedWithTimeout - wait until the MariaDBDatabase and MariaDBAccounts are
// initialized and reports Status.Conditions.IsTrue(MariaDBDatabaseReadyCondition)
// and Status.Conditions.IsTrue(MariaDBAccountReadyCondition)
func (d *Database) WaitForDBCreatedWithTimeout(
	ctx context.Context,
	h *helper.Helper,
	requeueAfter time.Duration,
) (ctrl.Result, error) {

	err := d.getDBWithName(
		ctx,
		h,
	)
	if err != nil && !k8s_errors.IsNotFound(err) {
		return ctrl.Result{}, err
	}

	if !d.database.Status.Conditions.IsTrue(MariaDBDatabaseReadyCondition) {
		util.LogForObject(
			h,
			fmt.Sprintf("Waiting for service DB %s to be created", d.database.Name),
			d.database,
		)

		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}

	if d.account != nil && !d.account.Status.Conditions.IsTrue(MariaDBAccountReadyCondition) {
		util.LogForObject(
			h,
			fmt.Sprintf("Waiting for service account %s to be created", d.account.Name),
			d.account,
		)

		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}

	if k8s_errors.IsNotFound(err) {
		util.LogForObject(
			h,
			fmt.Sprintf("DB or account objects not yet found %s", d.database.Name),
			d.database,
		)

		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}

	return ctrl.Result{}, nil
}

// WaitForDBCreated - wait until the MariaDBDatabase is initialized and reports Status.Completed == true
// Deprecated, use WaitForDBCreatedWithTimeout instead
func (d *Database) WaitForDBCreated(
	ctx context.Context,
	h *helper.Helper,
) (ctrl.Result, error) {
	return d.WaitForDBCreatedWithTimeout(ctx, h, time.Second*5)
}

// getDBWithName - get DB object with name in namespace
// note this is legacy as a new function will be added that allows for
// lookup of Database based on mariadbdatabase name and mariadbaccount name
// individually
func (d *Database) getDBWithName(
	ctx context.Context,
	h *helper.Helper,
) error {
	db := &MariaDBDatabase{}
	name := d.name
	namespace := d.namespace
	if name == "" {
		name = h.GetBeforeObject().GetName()
	}
	if namespace == "" {
		namespace = h.GetBeforeObject().GetNamespace()
	}

	err := h.GetClient().Get(
		ctx,
		types.NamespacedName{
			Name:      name,
			Namespace: namespace,
		},
		db)

	if err != nil {
		if k8s_errors.IsNotFound(err) {
			return util.WrapErrorForObject(
				fmt.Sprintf("Failed to get %s database %s ", name, namespace),
				h.GetBeforeObject(),
				err,
			)
		}

		return util.WrapErrorForObject(
			fmt.Sprintf("DB error %s %s ", name, namespace),
			h.GetBeforeObject(),
			err,
		)
	}

	d.database = db

	accountName := d.accountName

	legacyAccount := false

	if accountName == "" {
		// no account name, so this is a legacy lookup.  locate MariaDBAccount
		// based on the same name as that of the MariaDBDatabase
		accountName = d.name
		legacyAccount = true
	}

	account, err := GetAccount(ctx, h, accountName, d.namespace)

	if err != nil {
		if legacyAccount && k8s_errors.IsNotFound(err) {
			// if account can't be found, log it, but don't quit, still
			// return the Database with MariaDBDatabase
			h.GetLogger().Info(
				fmt.Sprintf("Could not find account %s for Database named %s", accountName, namespace),
			)

			// note that d.account remains nil in this case
		}

		return util.WrapErrorForObject(
			fmt.Sprintf("account error %s %s ", accountName, namespace),
			h.GetBeforeObject(),
			err,
		)
	} else {
		d.account = account
		d.databaseUser = account.Spec.UserName
		d.secret = account.Spec.Secret
	}

	return nil
}

// GetDatabaseByName returns a *Database object with specified name and namespace
// deprecated; this needs to have the account name given as well for it to work
// completely
func GetDatabaseByName(
	ctx context.Context,
	h *helper.Helper,
	name string,
) (*Database, error) {
	// create a Database by suppplying a resource name
	db := &Database{
		name: name,
	}
	// then querying the MariaDBDatabase and store it in db by calling
	if err := db.getDBWithName(ctx, h); err != nil {
		return db, err
	}
	return db, nil
}

func GetDatabaseByNameAndAccount(
	ctx context.Context,
	h *helper.Helper,
	name string,
	accountName string,
	namespace string,
) (*Database, error) {
	// create a Database by suppplying a resource name
	db := &Database{
		name:        name,
		accountName: accountName,
		namespace:   namespace,
	}
	// then querying the MariaDBDatabase and store it in db by calling
	if err := db.getDBWithName(ctx, h); err != nil {
		return db, err
	}
	return db, nil
}

// DeleteFinalizer deletes a finalizer by its object
func (d *Database) DeleteFinalizer(
	ctx context.Context,
	h *helper.Helper,
) error {

	if d.account != nil && controllerutil.RemoveFinalizer(d.account, h.GetFinalizer()) {
		err := h.GetClient().Update(ctx, d.account)
		if err != nil && !k8s_errors.IsNotFound(err) {
			return err
		}
		util.LogForObject(h, fmt.Sprintf("Removed finalizer %s from MariaDBAccount object", h.GetFinalizer()), d.account)
	}
	if controllerutil.RemoveFinalizer(d.database, h.GetFinalizer()) {
		err := h.GetClient().Update(ctx, d.database)
		if err != nil && !k8s_errors.IsNotFound(err) {
			return err
		}
		util.LogForObject(h, fmt.Sprintf("Removed finalizer %s from MariaDBDatabase object", h.GetFinalizer()), d.database)
	}
	return nil
}

// DeleteUnusedMariaDBAccountFinalizers searches for all MariaDBAccounts
// associated with the given MariaDBDabase name and removes the finalizer for all
// of them except for the given named account.
func DeleteUnusedMariaDBAccountFinalizers(
	ctx context.Context,
	h *helper.Helper,
	mariaDBDatabaseName string,
	mariaDBAccountName string,
	namespace string,
) error {

	accountList := &MariaDBAccountList{}

	opts := []client.ListOption{
		client.InNamespace(namespace),
		client.MatchingLabels{"mariaDBDatabaseName": mariaDBDatabaseName},
		client.MatchingFields{"kind": "MariaDBAccount"},
	}

	err := h.GetClient().List(ctx, accountList, opts...)

	if err != nil {
		return err
	}

	for _, mariaDBAccount := range accountList.Items {
		if mariaDBAccount.Name == mariaDBAccountName {
			continue
		}

		if controllerutil.RemoveFinalizer(&mariaDBAccount, h.GetFinalizer()) {
			err := h.GetClient().Update(ctx, &mariaDBAccount)
			if err != nil && !k8s_errors.IsNotFound(err) {
				return err
			}
			util.LogForObject(h, fmt.Sprintf("Removed finalizer %s from MariaDBAccount object", h.GetFinalizer()), &mariaDBAccount)
		}

	}
	return nil

}

func CreateOrPatchAccount(
	ctx context.Context,
	h *helper.Helper,
	account *MariaDBAccount,
	labels map[string]string,
) (controllerutil.OperationResult, error) {
	op_acc, err_acc := controllerutil.CreateOrPatch(ctx, h.GetClient(), account, func() error {
		account.Labels = util.MergeStringMaps(
			account.GetLabels(),
			labels,
		)

		err := controllerutil.SetControllerReference(h.GetBeforeObject(), account, h.GetScheme())
		if err != nil {
			return err
		}

		// If the service object doesn't have our finalizer, add it.
		controllerutil.AddFinalizer(account, h.GetFinalizer())

		return nil
	})

	return op_acc, err_acc
}

func GetAccount(ctx context.Context,
	h *helper.Helper,
	accountName string, namespace string,
) (*MariaDBAccount, error) {
	databaseAccount := &MariaDBAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      accountName,
			Namespace: namespace,
		},
	}
	objectKey := client.ObjectKeyFromObject(databaseAccount)

	err := h.GetClient().Get(ctx, objectKey, databaseAccount)
	if err != nil {
		return nil, err
	}
	return databaseAccount, err
}

// EnsureMariaDBAccount ensures a MariaDBAccount has been created for a given
// operator calling the function, and returns the MariaDBAccount and its
// Secret for use in consumption into a configuration.
// The current version of the function creates the objects if they don't
// exist; a later version of this can be set to only ensure that the objects
// were already created by an external actor such as openstack-operator up
// front.
func EnsureMariaDBAccount(ctx context.Context,
	helper *helper.Helper,
	accountName string, username string, namespace string, requireTLS bool,
) (*MariaDBAccount, *corev1.Secret, error) {

	account, err := GetAccount(ctx, helper, accountName, namespace)

	if err != nil {
		if !k8s_errors.IsNotFound(err) {
			return nil, nil, err
		}

		account = &MariaDBAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      accountName,
				Namespace: namespace,
				// note no labels yet; the account will not have a
				// mariadbdatabase yet so the controller will not
				// try to create a DB; it instead will respond again to the
				// MariaDBAccount once this is filled in
			},
			Spec: MariaDBAccountSpec{
				UserName:   username,
				Secret:     accountName,
				RequireTLS: requireTLS,
			},
		}

	} else {
		account.Spec.UserName = username
		account.Spec.RequireTLS = requireTLS

		if account.Spec.Secret == "" {
			account.Spec.Secret = accountName
		}
	}

	dbSecret, _, err := secret.GetSecret(ctx, helper, account.Spec.Secret, namespace)

	if err != nil {
		if !k8s_errors.IsNotFound(err) {
			return nil, nil, err
		}

		dbPassword, err := interimGenerateDBPassword()
		if err != nil {
			return nil, nil, err
		}

		dbSecret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      account.Spec.Secret,
				Namespace: namespace,
			},
			StringData: map[string]string{
				"DatabasePassword": dbPassword,
			},
		}

	}

	_, _, err_secret := secret.CreateOrPatchSecret(
		ctx,
		helper,
		helper.GetBeforeObject(),
		dbSecret,
	)
	if err_secret != nil {
		return nil, nil, err_secret
	}

	_, err_acc := CreateOrPatchAccount(ctx, helper, account, map[string]string{})
	if err_acc != nil {
		return nil, nil, err_acc
	}

	return account, dbSecret, nil
}

func interimGenerateDBPassword() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
