# Copyright 2020 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#      http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

require "sinatra"
get "/" do
  if not File.symlink?("tmp")
    halt 500, "tmp symlink does not exist"
  end
  if File.readlink("tmp") != "/tmp"
    halt 500, "tmp symlink does not point to /tmp"
  end
  if not File.symlink?("log")
    halt 500, "log symlink does not exist"
  end
  if File.readlink("log") != "/var/log"
    halt 500, "log symlink does not point to /var/log"
  end
  if !File.directory?("public/assets")
    halt 500, "public/assets does not exist"
  end
  if !File.file?("public/assets/.sprockets-manifest-1234.json")
    halt 500, "precompiled public asset does not exist"
  end
  "PASS"
end
