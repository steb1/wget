# Custom Wget Implementation

## Overview

This project is a custom implementation of some functionalities of the `wget` command-line tool. It is developed in Go and is designed to provide essential features for downloading files from the web, including downloading entire websites for offline access.

## Features

- **Single File Download**: Download a file from a given URL.
  - Example: `wget https://some_url.org/file.zip`
  
- **Custom File Naming**: Download a file and save it under a different name.
  - Example: `wget -O custom_name.zip https://some_url.org/file.zip`

- **Save to Specific Directory**: Save the downloaded file in a specified directory.
  - Example: `wget -P /path/to/directory https://some_url.org/file.zip`

- **Download Speed Limiting**: Limit the download speed to a specified rate.
  - Example: `wget --limit-rate=200k https://some_url.org/file.zip`

- **Background Downloading**: Download files in the background.
  - Example: `wget -b https://some_url.org/file.zip`

- **Asynchronous Multi-file Downloading**: Download multiple files simultaneously by reading a file containing multiple URLs.
  - Example: `wget -i urls.txt`

- **Website Mirroring**: Download an entire website, preserving the directory structure for offline viewing.
  - Example: `wget --mirror https://some_website.org`

## Installation

To install the program, follow these steps:

1. Clone the repository:
   ```bash
   git clone https://learn.zone01dakar.sn/git/lomalack/wget-test.git
