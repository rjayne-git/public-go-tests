# tag.py
# rjayne 2020

import argparse
import datetime
import os

import ics.git
import ics.proc
import ics.script

def RunListTags(args) -> bool:
    print('*** List Tags ***')
    cwd = os.getcwd()
    return ics.git.ListTags(cwd, verbose=args.verbose)

def RunTag(args) -> bool:
    if not args.tag:
        print("ERROR: Missing tag")
        return False
    if not args.object:
        print("ERROR: Missing object")
        return False
    tag = None
    if args.datetime:
        # Add local datetime to tag, and make valid git tag.
        tag = '{}_{}'.format(args.tag, datetime.datetime.now().strftime('%Y%m%d_%H%M%S'))
    else:
        tag = args.tag
    print('*** Tag Repo, tag="{}", obj="{}" ***'.format(tag, args.object))
    cwd = os.getcwd()
    return ics.git.Tag(cwd, tag, args.object, list=True, verbose=args.verbose)

def RunReTag(args) -> bool:
    if not args.tag:
        print("ERROR: Missing tag")
        return False
    if not args.object:
        print("ERROR: Missing object")
        return False
    print('*** Re-Tag Repo, tag="{}", obj="{}" ***'.format(args.tag, args.object))
    cwd = os.getcwd()
    return ics.git.ReTag(cwd, args.tag, args.object, list=True, verbose=args.verbose)

def RunDeleteTag(args) -> bool:
    if not args.tag:
        print("ERROR: Missing tag")
        return False
    print('*** Delete Tag from Repo, tag="{}" ***'.format(args.tag))
    cwd = os.getcwd()
    return ics.git.DeleteTag(cwd, args.tag, list=True, verbose=args.verbose)

def RunAutoTag(args) -> bool:
    if not args.object:
        print("ERROR: Missing object")
        return False
    cwd = os.getcwd()
    project = os.path.basename(cwd)
    return ics.git.AutoTag(project, cwd, args.object, list=True, verbose=args.verbose)

def RunAutoReTag(args) -> bool:
    if not args.object:
        print("ERROR: Missing object")
        return False
    cwd = os.getcwd()
    project = os.path.basename(cwd)
    return ics.git.AutoReTag(project, cwd, args.object, list=True, verbose=args.verbose)

def RunAutoDeleteTag(args) -> bool:
    cwd = os.getcwd()
    project = os.path.basename(cwd)
    return ics.git.AutoDeleteTag(project, cwd, list=True, verbose=args.verbose)

def RunAutoCommit(args) -> bool:
    if args.message is None:
        print("ERROR: Missing message")
        return False
    print('*** Auto Commit Repo, parentbranch="{}" message="{}" ***'.format(args.parentbranch, args.message))
    cwd = os.getcwd()
    ics.git.AutoCommit(cwd, args.parentbranch, args.message, verbose=args.verbose)
    return True

def ScriptRun():
    # ArgumentParser
    parser = argparse.ArgumentParser()
    parser.add_argument('-v', '--verbose', help='increase output verbosity', action='store_true')
    subparsers = parser.add_subparsers(title='Supported subcommands')
    # RunListTags
    parser_runlist = subparsers.add_parser('list')
    parser_runlist.set_defaults(func=RunListTags)
    # RunTag
    parser_runtag = subparsers.add_parser('tag')
    parser_runtag.add_argument('-t', '--tag', help='git tag label', required=True)
    parser_runtag.add_argument('-obj', '--object', help='git tag object (or branch), default="HEAD"', default='HEAD')
    parser_runtag.add_argument('-dt', '--datetime', help='append local datetime to git tag label', action='store_true')
    parser_runtag.set_defaults(func=RunTag)
    # RunReTag
    parser_runretag = subparsers.add_parser('re-tag')
    parser_runretag.add_argument('-t', '--tag', help='git tag label', required=True)
    parser_runretag.add_argument('-obj', '--object', help='git tag object (or branch), default="HEAD"', default='HEAD')
    parser_runretag.set_defaults(func=RunReTag)
    # RunDeleteTag
    parser_rundeletetag = subparsers.add_parser('delete-tag')
    parser_rundeletetag.add_argument('-t', '--tag', help='git tag label', required=True)
    parser_rundeletetag.set_defaults(func=RunDeleteTag)
    # RunAutoTag
    parser_runautotag = subparsers.add_parser('auto-tag')
    parser_runautotag.add_argument('-obj', '--object', help='git tag object (or branch), default="HEAD"', default='HEAD')
    parser_runautotag.set_defaults(func=RunAutoTag)
    # RunAutoReTag
    parser_runautoretag = subparsers.add_parser('auto-re-tag')
    parser_runautoretag.add_argument('-obj', '--object', help='git tag object (or branch), default="HEAD"', default='HEAD')
    parser_runautoretag.set_defaults(func=RunAutoReTag)
    # RunAutoDeleteTag
    parser_runautodeletetag = subparsers.add_parser('auto-delete-tag')
    parser_runautodeletetag.set_defaults(func=RunAutoDeleteTag)
    # RunAutoCommit
    parser_runautocommit = subparsers.add_parser('auto-commit')
    parser_runautocommit.add_argument('-m', '--message', help='git commit message', required=True)
    parser_runautocommit.add_argument('-pb', '--parentbranch', help='origin parent branch to pull, like "main" or "rc/ms"', required=True)
    parser_runautocommit.set_defaults(func=RunAutoCommit)
    # Parse args
    args = parser.parse_args()
    # Run args function
    ics.script.RunScriptArgsFunc(os.path.basename(__file__), args)

# Run
ScriptRun()
