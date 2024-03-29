/*
Copyright 2021-2022 Red Hat, Inc.

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
// Code generated by client-gen. DO NOT EDIT.

package fake

import (
	"context"

	v1alpha1 "github.com/apheleia-project/apheleia/pkg/apis/apheleia/v1alpha1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	labels "k8s.io/apimachinery/pkg/labels"
	schema "k8s.io/apimachinery/pkg/runtime/schema"
	types "k8s.io/apimachinery/pkg/types"
	watch "k8s.io/apimachinery/pkg/watch"
	testing "k8s.io/client-go/testing"
)

// FakeComponentBuilds implements ComponentBuildInterface
type FakeComponentBuilds struct {
	Fake *FakeApheleiaV1alpha1
	ns   string
}

var componentbuildsResource = schema.GroupVersionResource{Group: "apheleia.io", Version: "v1alpha1", Resource: "componentbuilds"}

var componentbuildsKind = schema.GroupVersionKind{Group: "apheleia.io", Version: "v1alpha1", Kind: "ComponentBuild"}

// Get takes name of the componentBuild, and returns the corresponding componentBuild object, and an error if there is any.
func (c *FakeComponentBuilds) Get(ctx context.Context, name string, options v1.GetOptions) (result *v1alpha1.ComponentBuild, err error) {
	obj, err := c.Fake.
		Invokes(testing.NewGetAction(componentbuildsResource, c.ns, name), &v1alpha1.ComponentBuild{})

	if obj == nil {
		return nil, err
	}
	return obj.(*v1alpha1.ComponentBuild), err
}

// List takes label and field selectors, and returns the list of ComponentBuilds that match those selectors.
func (c *FakeComponentBuilds) List(ctx context.Context, opts v1.ListOptions) (result *v1alpha1.ComponentBuildList, err error) {
	obj, err := c.Fake.
		Invokes(testing.NewListAction(componentbuildsResource, componentbuildsKind, c.ns, opts), &v1alpha1.ComponentBuildList{})

	if obj == nil {
		return nil, err
	}

	label, _, _ := testing.ExtractFromListOptions(opts)
	if label == nil {
		label = labels.Everything()
	}
	list := &v1alpha1.ComponentBuildList{ListMeta: obj.(*v1alpha1.ComponentBuildList).ListMeta}
	for _, item := range obj.(*v1alpha1.ComponentBuildList).Items {
		if label.Matches(labels.Set(item.Labels)) {
			list.Items = append(list.Items, item)
		}
	}
	return list, err
}

// Watch returns a watch.Interface that watches the requested componentBuilds.
func (c *FakeComponentBuilds) Watch(ctx context.Context, opts v1.ListOptions) (watch.Interface, error) {
	return c.Fake.
		InvokesWatch(testing.NewWatchAction(componentbuildsResource, c.ns, opts))

}

// Create takes the representation of a componentBuild and creates it.  Returns the server's representation of the componentBuild, and an error, if there is any.
func (c *FakeComponentBuilds) Create(ctx context.Context, componentBuild *v1alpha1.ComponentBuild, opts v1.CreateOptions) (result *v1alpha1.ComponentBuild, err error) {
	obj, err := c.Fake.
		Invokes(testing.NewCreateAction(componentbuildsResource, c.ns, componentBuild), &v1alpha1.ComponentBuild{})

	if obj == nil {
		return nil, err
	}
	return obj.(*v1alpha1.ComponentBuild), err
}

// Update takes the representation of a componentBuild and updates it. Returns the server's representation of the componentBuild, and an error, if there is any.
func (c *FakeComponentBuilds) Update(ctx context.Context, componentBuild *v1alpha1.ComponentBuild, opts v1.UpdateOptions) (result *v1alpha1.ComponentBuild, err error) {
	obj, err := c.Fake.
		Invokes(testing.NewUpdateAction(componentbuildsResource, c.ns, componentBuild), &v1alpha1.ComponentBuild{})

	if obj == nil {
		return nil, err
	}
	return obj.(*v1alpha1.ComponentBuild), err
}

// UpdateStatus was generated because the type contains a Status member.
// Add a +genclient:noStatus comment above the type to avoid generating UpdateStatus().
func (c *FakeComponentBuilds) UpdateStatus(ctx context.Context, componentBuild *v1alpha1.ComponentBuild, opts v1.UpdateOptions) (*v1alpha1.ComponentBuild, error) {
	obj, err := c.Fake.
		Invokes(testing.NewUpdateSubresourceAction(componentbuildsResource, "status", c.ns, componentBuild), &v1alpha1.ComponentBuild{})

	if obj == nil {
		return nil, err
	}
	return obj.(*v1alpha1.ComponentBuild), err
}

// Delete takes name of the componentBuild and deletes it. Returns an error if one occurs.
func (c *FakeComponentBuilds) Delete(ctx context.Context, name string, opts v1.DeleteOptions) error {
	_, err := c.Fake.
		Invokes(testing.NewDeleteActionWithOptions(componentbuildsResource, c.ns, name, opts), &v1alpha1.ComponentBuild{})

	return err
}

// DeleteCollection deletes a collection of objects.
func (c *FakeComponentBuilds) DeleteCollection(ctx context.Context, opts v1.DeleteOptions, listOpts v1.ListOptions) error {
	action := testing.NewDeleteCollectionAction(componentbuildsResource, c.ns, listOpts)

	_, err := c.Fake.Invokes(action, &v1alpha1.ComponentBuildList{})
	return err
}

// Patch applies the patch and returns the patched componentBuild.
func (c *FakeComponentBuilds) Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts v1.PatchOptions, subresources ...string) (result *v1alpha1.ComponentBuild, err error) {
	obj, err := c.Fake.
		Invokes(testing.NewPatchSubresourceAction(componentbuildsResource, c.ns, name, pt, data, subresources...), &v1alpha1.ComponentBuild{})

	if obj == nil {
		return nil, err
	}
	return obj.(*v1alpha1.ComponentBuild), err
}
