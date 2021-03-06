package deploy

import (
	"context"
	"fmt"

	olmv1 "github.com/operator-framework/api/pkg/operators/v1"
	olmv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
)

func (deploy *Deployer) installNamespace() error {
	// TODO: we can further cleanup this method by separating the
	// creation of the namespace object and the label/annotation
	// handling by always treating the latter as an update:
	// https://github.com/kube-reporting/metering-operator/pull/1270#discussion_r444436226
	namespace, err := deploy.client.CoreV1().Namespaces().Get(context.TODO(), deploy.config.Namespace, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		namespaceObjectMeta := metav1.ObjectMeta{
			Name: deploy.config.Namespace,
		}

		labels := make(map[string]string)

		for key, val := range deploy.config.ExtraNamespaceLabels {
			labels[key] = val
			deploy.logger.Infof("Labeling the %s namespace with '%s=%s'", deploy.config.Namespace, key, val)
		}

		/*
			In the case where the platform is set to Openshift (the default value),
			we need to make a few modifications to the namespace metadata.
			The 'openshift.io/cluster-monitoring' labels tells the cluster-monitoring
			operator to scrape Prometheus metrics for the installed Metering namespace.
			The 'openshift.io/node-selector' annotation is a way to control where Pods
			get scheduled in a specific namespace. If this annotation is set to an empty
			label, that means that Pods for this namespace can be scheduled on any nodes.
			In the case where a cluster administrator has configured a value for the
			defaultNodeSelector field in the cluster's Scheduler object, we need to set
			this namespace annotation in order to avoid a collision with what the user
			has supplied in their MeteringConfig custom resource. This implies that whenever
			a cluster has been configured to schedule Pods using a default node selector,
			those changes must also be propogated to the MeteringConfig custom resource, else
			the Pods in Metering namespace will be scheduled on any available node.
		*/
		if deploy.config.Platform == "openshift" {
			labels["openshift.io/cluster-monitoring"] = "true"
			deploy.logger.Infof("Labeling the %s namespace with 'openshift.io/cluster-monitoring=true'", deploy.config.Namespace)
			namespaceObjectMeta.Annotations = map[string]string{
				"openshift.io/node-selector": "",
			}
			deploy.logger.Infof("Annotating the %s namespace with 'openshift.io/node-selector=''", deploy.config.Namespace)
		}

		namespaceObjectMeta.Labels = labels
		namespaceObj := &v1.Namespace{
			ObjectMeta: namespaceObjectMeta,
		}

		_, err := deploy.client.CoreV1().Namespaces().Create(context.TODO(), namespaceObj, metav1.CreateOptions{})
		if err != nil {
			return err
		}
		deploy.logger.Infof("Created the %s namespace", deploy.config.Namespace)
		return nil
	}
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}

	// check if we need to add/update the cluster-monitoring label for Openshift installs.
	if deploy.config.Platform == "openshift" {
		if namespace.ObjectMeta.Labels != nil {
			namespace.ObjectMeta.Labels["openshift.io/cluster-monitoring"] = "true"
			deploy.logger.Infof("Updated the 'openshift.io/cluster-monitoring' label to the %s namespace", deploy.config.Namespace)
		} else {
			namespace.ObjectMeta.Labels = map[string]string{
				"openshift.io/cluster-monitoring": "true",
			}
			deploy.logger.Infof("Added the 'openshift.io/cluster-monitoring' label to the %s namespace", deploy.config.Namespace)
		}
		if namespace.ObjectMeta.Annotations != nil {
			namespace.ObjectMeta.Annotations["openshift.io/node-selector"] = ""
			deploy.logger.Infof("Updated the 'openshift.io/node-selector' annotation to the %s namespace", deploy.config.Namespace)
		} else {
			namespace.ObjectMeta.Annotations = map[string]string{
				"openshift.io/node-selector": "",
			}
			deploy.logger.Infof("Added the empty 'openshift.io/node-selector' annotation to the %s namespace", deploy.config.Namespace)
		}

		_, err := deploy.client.CoreV1().Namespaces().Update(context.TODO(), namespace, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("failed to add the 'openshift.io/cluster-monitoring' label to the %s namespace: %v", deploy.config.Namespace, err)
		}
		return nil
	}

	// TODO: handle updating the namespace for non-openshift installations
	deploy.logger.Infof("The %s namespace already exists", deploy.config.Namespace)

	return nil
}

func (deploy *Deployer) installMeteringConfig() error {
	if deploy.config.MeteringConfig == nil {
		return fmt.Errorf("invalid deploy configuration: MeteringConfig object is nil")
	}
	if deploy.config.MeteringConfig.Name == "" {
		return fmt.Errorf("invalid deploy configuration: metadata.Name is unset")
	}

	// ensure the MeteringConfig CRD has already been created to avoid
	// any errors while instantiating a MeteringConfig custom resource
	err := wait.Poll(crdInitialPoll, crdPollTimeout, func() (done bool, err error) {
		mc, err := deploy.apiExtClient.CustomResourceDefinitions().Get(context.TODO(), meteringconfigCRDName, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			deploy.logger.Infof("Waiting for the MeteringConfig CRD to be created")
			return false, nil
		}
		if err != nil {
			return false, err
		}
		// in order to handle the following error, ensure the Status field has a populated entry for the "plural" CRD name:
		// the server could not find the requested resource (post meteringconfigs.metering.openshift.io)
		if mc.Status.AcceptedNames.Plural != "meteringconfigs" {
			deploy.logger.Infof("Waiting for the MeteringConfig CRD to be ready")
			return false, nil
		}

		return true, nil
	})
	if err != nil {
		return fmt.Errorf("failed to wait for the MeteringConfig CRD to be created: %v", err)
	}
	deploy.logger.Infof("The MeteringConfig CRD exists")

	mc, err := deploy.meteringClient.MeteringConfigs(deploy.config.Namespace).Get(context.TODO(), deploy.config.MeteringConfig.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = deploy.meteringClient.MeteringConfigs(deploy.config.Namespace).Create(context.TODO(), deploy.config.MeteringConfig, metav1.CreateOptions{})
		if err != nil {
			return err
		}
		deploy.logger.Infof("Created the MeteringConfig resource")
		return nil
	}
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}

	mc.Spec = deploy.config.MeteringConfig.Spec

	_, err = deploy.meteringClient.MeteringConfigs(deploy.config.Namespace).Update(context.TODO(), mc, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update the MeteringConfig: %v", err)
	}
	deploy.logger.Infof("The MeteringConfig resource has been updated")

	return nil
}

func (deploy *Deployer) installMeteringOperatorGroup() error {
	deployNamespace := deploy.config.Namespace

	opgrp, err := deploy.olmV1Client.OperatorGroups(deployNamespace).Get(context.TODO(), deployNamespace, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		opgrp := &olmv1.OperatorGroup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      deployNamespace,
				Namespace: deployNamespace,
			},
			Spec: olmv1.OperatorGroupSpec{
				TargetNamespaces: []string{
					deployNamespace,
				},
			},
		}

		_, err = deploy.olmV1Client.OperatorGroups(deployNamespace).Create(context.TODO(), opgrp, metav1.CreateOptions{})
		if err != nil {
			return err
		}
		deploy.logger.Infof("Created the %s metering OperatorGroup in the %s namespace", opgrp.Name, deployNamespace)
		return nil
	}
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	deploy.logger.Infof("The %s metering OperatorGroup resource already exists", opgrp.Name)

	return nil
}

func (deploy *Deployer) installMeteringSubscription() error {
	deployNamespace := deploy.config.Namespace
	subName := deploy.config.SubscriptionName

	_, err := deploy.olmV1Alpha1Client.Subscriptions(deployNamespace).Get(context.TODO(), subName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		sub := &olmv1alpha1.Subscription{
			ObjectMeta: metav1.ObjectMeta{
				Name:      subName,
				Namespace: deployNamespace,
			},
			Spec: &olmv1alpha1.SubscriptionSpec{
				CatalogSource:          deploy.config.CatalogSourceName,
				CatalogSourceNamespace: deploy.config.CatalogSourceNamespace,
				Package:                deploy.config.PackageName,
				Channel:                deploy.config.Channel,
				InstallPlanApproval:    olmv1alpha1.ApprovalAutomatic,
			},
		}

		_, err := deploy.olmV1Alpha1Client.Subscriptions(deployNamespace).Create(context.TODO(), sub, metav1.CreateOptions{})
		if err != nil {
			return err
		}
		deploy.logger.Infof("Created the %s metering Subscription in the %s namespace", subName, deployNamespace)
		return nil
	}
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	deploy.logger.Infof("The %s metering Subscription in the %s namespace already exists", subName, deployNamespace)

	return nil
}

func (deploy *Deployer) installMeteringResources() error {
	if !deploy.config.RunMeteringOperatorLocal {
		err := deploy.installMeteringDeployment()
		if err != nil {
			return fmt.Errorf("failed to create the metering deployment: %v", err)
		}
	}

	err := deploy.installMeteringServiceAccount()
	if err != nil {
		return fmt.Errorf("failed to create the metering service account: %v", err)
	}

	err = deploy.installMeteringRole()
	if err != nil {
		return fmt.Errorf("failed to create the metering role: %v", err)
	}

	err = deploy.installMeteringRoleBinding()
	if err != nil {
		return fmt.Errorf("failed to create the metering role binding: %v", err)
	}

	err = deploy.installMeteringClusterRole()
	if err != nil {
		return fmt.Errorf("failed to create the metering cluster role: %v", err)
	}

	err = deploy.installMeteringClusterRoleBinding()
	if err != nil {
		return fmt.Errorf("failed to create the metering cluster role binding: %v", err)
	}

	return nil
}

func (deploy *Deployer) installMeteringDeployment() error {
	res := deploy.config.OperatorResources.Deployment

	// check if the metering operator image needs to be updated
	// TODO: implement support for METERING_OPERATOR_ALL_NAMESPACES and METERING_OPERATOR_TARGET_NAMESPACES
	if deploy.config.Repo != "" && deploy.config.Tag != "" {
		newImage := deploy.config.Repo + ":" + deploy.config.Tag

		for index := range res.Spec.Template.Spec.Containers {
			res.Spec.Template.Spec.Containers[index].Image = newImage
		}

		deploy.logger.Infof("Overriding the default image with %s", newImage)
	}

	deployment, err := deploy.client.AppsV1().Deployments(deploy.config.Namespace).Get(context.TODO(), res.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err := deploy.client.AppsV1().Deployments(deploy.config.Namespace).Create(context.TODO(), res, metav1.CreateOptions{})
		if err != nil {
			return err
		}
		deploy.logger.Infof("Created the metering deployment")
		return nil
	}
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}

	deployment.Spec = res.Spec

	_, err = deploy.client.AppsV1().Deployments(deploy.config.Namespace).Update(context.TODO(), deployment, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update the metering deployment: %v", err)
	}
	deploy.logger.Infof("The metering deployment resource has been updated")

	return nil
}

func (deploy *Deployer) installMeteringServiceAccount() error {
	_, err := deploy.client.CoreV1().ServiceAccounts(deploy.config.Namespace).Get(context.TODO(), deploy.config.OperatorResources.ServiceAccount.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err := deploy.client.CoreV1().ServiceAccounts(deploy.config.Namespace).Create(context.TODO(), deploy.config.OperatorResources.ServiceAccount, metav1.CreateOptions{})
		if err != nil {
			return err
		}
		deploy.logger.Infof("Created the metering serviceaccount")
		return nil
	}
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}

	deploy.logger.Infof("The metering service account already exists")

	return nil
}

func (deploy *Deployer) installMeteringRoleBinding() error {
	res := deploy.config.OperatorResources.RoleBinding
	// TODO: implement support for METERING_OPERATOR_TARGET_NAMESPACES
	res.Name = deploy.config.Namespace + "-" + res.Name
	res.RoleRef.Name = res.Name
	res.Namespace = deploy.config.Namespace

	for index := range res.Subjects {
		res.Subjects[index].Namespace = deploy.config.Namespace
	}

	_, err := deploy.client.RbacV1().RoleBindings(deploy.config.Namespace).Get(context.TODO(), res.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err := deploy.client.RbacV1().RoleBindings(deploy.config.Namespace).Create(context.TODO(), res, metav1.CreateOptions{})
		if err != nil {
			return err
		}
		deploy.logger.Infof("Created the metering role binding")
		return nil
	}
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}

	deploy.logger.Infof("The metering role binding already exists")

	return nil
}

func (deploy *Deployer) installMeteringRole() error {
	res := deploy.config.OperatorResources.Role
	res.Name = deploy.config.Namespace + "-" + res.Name
	res.Namespace = deploy.config.Namespace

	_, err := deploy.client.RbacV1().Roles(deploy.config.Namespace).Get(context.TODO(), res.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err := deploy.client.RbacV1().Roles(deploy.config.Namespace).Create(context.TODO(), res, metav1.CreateOptions{})
		if err != nil {
			return err
		}
		deploy.logger.Infof("Created the metering role")
		return nil
	}
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}

	deploy.logger.Infof("The metering role already exists")

	return nil
}

func (deploy *Deployer) installMeteringClusterRoleBinding() error {
	res := deploy.config.OperatorResources.ClusterRoleBinding
	res.Name = deploy.config.Namespace + "-" + res.Name
	res.RoleRef.Name = res.Name

	for index := range res.Subjects {
		res.Subjects[index].Namespace = deploy.config.Namespace
	}

	_, err := deploy.client.RbacV1().ClusterRoleBindings().Get(context.TODO(), res.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err := deploy.client.RbacV1().ClusterRoleBindings().Create(context.TODO(), res, metav1.CreateOptions{})
		if err != nil {
			return err
		}
		deploy.logger.Infof("Created the metering cluster role binding")
		return nil
	}
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}

	_, err = deploy.client.RbacV1().ClusterRoleBindings().Update(context.TODO(), res, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update the metering clusterrolebinding: %v", err)
	}
	deploy.logger.Infof("The metering cluster role binding has been updated")

	return nil
}

func (deploy *Deployer) installMeteringClusterRole() error {
	res := deploy.config.OperatorResources.ClusterRole
	res.Name = deploy.config.Namespace + "-" + res.Name

	clusterRole, err := deploy.client.RbacV1().ClusterRoles().Get(context.TODO(), res.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err := deploy.client.RbacV1().ClusterRoles().Create(context.TODO(), res, metav1.CreateOptions{})
		if err != nil {
			return err
		}
		deploy.logger.Infof("Created the metering cluster role")
		return nil
	}
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}

	clusterRole.Rules = res.Rules
	_, err = deploy.client.RbacV1().ClusterRoles().Update(context.TODO(), clusterRole, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update the metering clusterrole: %v", err)
	}
	deploy.logger.Infof("The metering clusterrole has been updated")

	return err
}

func (deploy *Deployer) installMeteringCRDs() error {
	for _, crd := range deploy.config.OperatorResources.CRDs {
		err := deploy.installMeteringCRD(crd)
		if err != nil {
			return fmt.Errorf("failed to create a CRD while looping: %v", err)
		}
	}

	return nil
}

func (deploy *Deployer) installMeteringCRD(resource CRD) error {
	crd, err := deploy.apiExtClient.CustomResourceDefinitions().Get(context.TODO(), resource.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err := deploy.apiExtClient.CustomResourceDefinitions().Create(context.TODO(), resource.CRD, metav1.CreateOptions{})
		if err != nil {
			return err
		}
		deploy.logger.Infof("Created the %s CRD", resource.Name)
		return nil
	}
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}

	crd.Spec = resource.CRD.Spec
	_, err = deploy.apiExtClient.CustomResourceDefinitions().Update(context.TODO(), crd, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update the %s CRD: %v", resource.CRD.Name, err)
	}
	deploy.logger.Infof("Updated the %s CRD", resource.CRD.Name)

	return nil
}
