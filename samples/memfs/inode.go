// Copyright 2015 Google Inc. All Rights Reserved.
// Author: jacobsa@google.com (Aaron Jacobs)

package memfs

import (
	"fmt"
	"os"

	"github.com/jacobsa/fuse"
	"github.com/jacobsa/fuse/fuseutil"
	"github.com/jacobsa/gcloud/syncutil"
)

// Common attributes for files and directories.
//
// TODO(jacobsa): Add tests for interacting with a file/directory after it has
// been unlinked, including creating a new file. Make sure we don't screw up
// and reuse an inode ID while it is still in use.
type inode struct {
	/////////////////////////
	// Constant data
	/////////////////////////

	// Is this a directory? If not, it is a file.
	dir bool

	/////////////////////////
	// Mutable state
	/////////////////////////

	mu syncutil.InvariantMutex

	// The current attributes of this inode.
	//
	// INVARIANT: No non-permission mode bits are set besides os.ModeDir
	// INVARIANT: If dir, then os.ModeDir is set
	// INVARIANT: If !dir, then os.ModeDir is not set
	attributes fuse.InodeAttributes // GUARDED_BY(mu)

	// For directories, entries describing the children of the directory.
	//
	// This array can never be shortened, nor can its elements be moved, because
	// we use its indices for Dirent.Offset, which is exposed to the user who
	// might be calling readdir in a loop while concurrently modifying the
	// directory. Unused entries can, however, be reused.
	//
	// TODO(jacobsa): Add good tests exercising concurrent modifications while
	// doing readdir, seekdir, etc. calls.
	//
	// INVARIANT: If dir is false, this is nil.
	// INVARIANT: For each i, entries[i].Offset == i+1
	// INVARIANT: Contains no duplicate names.
	entries []fuseutil.Dirent // GUARDED_BY(mu)

	// For files, the current contents of the file.
	//
	// INVARIANT: If dir is true, this is nil.
	contents []byte // GUARDED_BY(mu)
}

////////////////////////////////////////////////////////////////////////
// Helpers
////////////////////////////////////////////////////////////////////////

func newInode(attrs fuse.InodeAttributes) (in *inode) {
	in = &inode{
		dir:        (attrs.Mode&os.ModeDir != 0),
		attributes: attrs,
	}

	in.mu = syncutil.NewInvariantMutex(in.checkInvariants)
	return
}

func (inode *inode) checkInvariants() {
	// No non-permission mode bits should be set besides os.ModeDir.
	if inode.attributes.Mode & ^(os.ModePerm|os.ModeDir) != 0 {
		panic(fmt.Sprintf("Unexpected mode: %v", inode.attributes.Mode))
	}

	// Check os.ModeDir.
	if inode.dir != (inode.attributes.Mode&os.ModeDir == os.ModeDir) {
		panic(
			fmt.Sprintf(
				"Unexpected mode: %v, dir: %v",
				inode.attributes.Mode,
				inode.dir))
	}

	// Check directory-specific stuff.
	if inode.dir {
		if inode.contents != nil {
			panic("Non-nil contents in a directory.")
		}

		childNames := make(map[string]struct{})
		for i, e := range inode.entries {
			if e.Offset != fuse.DirOffset(i+1) {
				panic(fmt.Sprintf("Unexpected offset: %v", e.Offset))
			}

			if _, ok := childNames[e.Name]; ok {
				panic(fmt.Sprintf("Duplicate name: %s", e.Name))
			}

			childNames[e.Name] = struct{}{}
		}
	}

	// Check file-specific stuff.
	if !inode.dir {
		if inode.entries != nil {
			panic("Non-nil entries in a file.")
		}
	}
}

////////////////////////////////////////////////////////////////////////
// Public methods
////////////////////////////////////////////////////////////////////////

// Find an entry for the given child name and return its inode ID.
//
// REQUIRES: inode.dir
// SHARED_LOCKS_REQUIRED(inode.mu)
func (inode *inode) LookUpChild(name string) (id fuse.InodeID, ok bool) {
	if !inode.dir {
		panic("LookUpChild called on non-directory.")
	}

	for _, e := range inode.entries {
		if e.Name == name {
			id = e.Inode
			ok = true
			return
		}
	}

	return
}

// Add an entry for a child.
//
// REQUIRES: inode.dir
// EXCLUSIVE_LOCKS_REQUIRED(inode.mu)
func (inode *inode) AddChild(
	id fuse.InodeID,
	name string,
	dt fuseutil.DirentType) {
	e := fuseutil.Dirent{
		Offset: fuse.DirOffset(len(inode.entries) + 1),
		Inode:  id,
		Name:   name,
		Type:   dt,
	}

	inode.entries = append(inode.entries, e)
}

// Serve a ReadDir request.
//
// REQUIRED: inode.dir
// SHARED_LOCKS_REQUIRED(inode.mu)
func (inode *inode) ReadDir(offset int, size int) (data []byte, err error) {
	if !inode.dir {
		panic("ReadDir called on non-directory.")
	}

	for i := offset; i < len(inode.entries); i++ {
		data = fuseutil.AppendDirent(data, inode.entries[i])

		// Trim and stop early if we've exceeded the requested size.
		if len(data) > size {
			data = data[:size]
			break
		}
	}

	return
}