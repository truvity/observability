package ec2

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// configPrefix is the directory, under Args.BucketPrefix in Args.Bucket, where
// the box's rendered files live as content-addressed objects. Litestream's
// replicas sit in sibling directories named after the instances, so no
// instance may be called this (validateInstances refuses it).
const configPrefix = "config"

// setupName names the setup script's object.
const setupName = "setup-script"

// trustedCAsName is the staged name of the extra CA bundle.
const trustedCAsName = "trusted-cas"

// configObject is one rendered file the box downloads at its first boot
// instead of carrying it in user-data. User-data is capped at 16 KiB, and two
// near-identical Gatus configurations for an estate-sized catalogue do not fit
// even gzipped (gzip of gzip saves nothing, and the duplication is paid
// twice). The key is derived from the content, so the sha256 in the user-data
// is what pins the object: a changed file is a new key, a new launch template
// version and a rolled instance, exactly as before.
type configObject struct {
	// Name is what the file is staged as (an instance name or trustedCAsName).
	Name string
	// Ext is the key's extension: yaml or pem.
	Ext     string
	Content string
}

// SHA256 is the lower-case hex digest of the content.
func (o configObject) SHA256() string {
	sum := sha256.Sum256([]byte(o.Content))

	return hex.EncodeToString(sum[:])
}

// Key is the object key inside the bucket.
func (o configObject) Key(bucketPrefix string) string {
	key := fmt.Sprintf("%s/%s.%s", configPrefix, o.SHA256(), o.Ext)
	if bucketPrefix != "" {
		key = bucketPrefix + "/" + key
	}

	return key
}

// setupObject is the setup script as an object: the largest file of the box.
func (a Args) setupObject() configObject {
	return configObject{Name: setupName, Ext: "sh", Content: setupScript}
}

// bucketObjects are every object NewEC2 creates: the setup script, then the
// configObjects, once per distinct content (two instances may render the same
// file; one object serves both).
func (a Args) bucketObjects() []configObject {
	var out []configObject

	seen := map[string]bool{}

	for _, o := range append([]configObject{a.setupObject()}, a.configObjects()...) {
		if seen[o.SHA256()] {
			continue
		}

		seen[o.SHA256()] = true

		out = append(out, o)
	}

	return out
}

// configObjects are the files the box fetches: every instance's Gatus
// configuration and, when set, the extra CA bundle. None holds a secret: a
// Config references its secrets as ${ENV} names the boot phase fills from SSM.
func (a Args) configObjects() []configObject {
	objs := make([]configObject, 0, len(a.Instances)+1)

	for _, inst := range a.Instances {
		objs = append(objs, configObject{Name: inst.Name, Ext: "yaml", Content: inst.Config})
	}

	if a.TrustedCAs != "" {
		objs = append(objs, configObject{Name: trustedCAsName, Ext: "pem", Content: a.TrustedCAs})
	}

	return objs
}

// configEntries renders the SB_CONFIGS array entries, name:sha256:key.
func (a Args) configEntries() []string {
	objs := a.configObjects()
	entries := make([]string, len(objs))

	for i, o := range objs {
		entries[i] = strings.Join([]string{o.Name, o.SHA256(), o.Key(a.BucketPrefix)}, ":")
	}

	return entries
}

// resourceSuffix names the object's Pulumi resource under the component.
func (o configObject) resourceSuffix() string {
	if o.Name == setupName {
		return setupName
	}

	return "config-" + o.Name
}
