The goal of this project is creating a GOLANG binary for fast vast.ai coding models deploying.
Check https://docs.vast.ai/api-reference/introduction, we automate  docs/general task.md


mycodeagent models 
 - list of the models
   - coding
   - fiction writing
   - dolphin
  
mycodeagent init <model name>
    Deploys model with autoloading from HF, establishes ssh tunnel with the port number, saves PID ~/.mycodeagent/sqlite db
    using a new port

mycodeagent ps - list of deployed instances PID -> Model Name
mycodeagent stop <PID>
    - stops the instance
mycodeagent <kill>
mycodeagent pull
    - pulls the list of instances from vast.ai


mycodeagent budget 
 - shows the consumption by instances


we use DDD/SOLID/Clean principles

Application service
Domain 
    Service
    Entity
    Repos

Infra
    Repos implementation
    API implementation