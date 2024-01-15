//go:build !ignore_autogenerated
// +build !ignore_autogenerated

/*
Copyright 2023.

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

// Code generated by controller-gen. DO NOT EDIT.

package v1

import (
	runtime "k8s.io/apimachinery/pkg/runtime"
)

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *Lbhc) DeepCopyInto(out *Lbhc) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	out.Status = in.Status
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new Lbhc.
func (in *Lbhc) DeepCopy() *Lbhc {
	if in == nil {
		return nil
	}
	out := new(Lbhc)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *Lbhc) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *LbhcList) DeepCopyInto(out *LbhcList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]Lbhc, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new LbhcList.
func (in *LbhcList) DeepCopy() *LbhcList {
	if in == nil {
		return nil
	}
	out := new(LbhcList)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *LbhcList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *LbhcSpec) DeepCopyInto(out *LbhcSpec) {
	*out = *in
	if in.DownEpList != nil {
		in, out := &in.DownEpList, &out.DownEpList
		*out = make(map[string]map[string]bool, len(*in))
		for key, val := range *in {
			var outVal map[string]bool
			if val == nil {
				(*out)[key] = nil
			} else {
				in, out := &val, &outVal
				*out = make(map[string]bool, len(*in))
				for key, val := range *in {
					(*out)[key] = val
				}
			}
			(*out)[key] = outVal
		}
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new LbhcSpec.
func (in *LbhcSpec) DeepCopy() *LbhcSpec {
	if in == nil {
		return nil
	}
	out := new(LbhcSpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *LbhcStatus) DeepCopyInto(out *LbhcStatus) {
	*out = *in
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new LbhcStatus.
func (in *LbhcStatus) DeepCopy() *LbhcStatus {
	if in == nil {
		return nil
	}
	out := new(LbhcStatus)
	in.DeepCopyInto(out)
	return out
}
